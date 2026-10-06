package ociutil

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/archive"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/pkg/compression"

	"github.com/operator-framework/library-olm/image"
)

// DiscoverManifestDescriptors parses manifestBytes as an OCI manifest and returns
// all descriptors that would be needed to unpack it: the manifest descriptor itself,
// the config blob, and all layers. It fetches the config blob to populate the cache
// but does not fetch layer blobs.
func DiscoverManifestDescriptors(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte) ([]ocispecv1.Descriptor, error) {
	var m ocispecv1.Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	descs := make([]ocispecv1.Descriptor, 0, 2+len(m.Layers))
	descs = append(descs, desc)
	descs = append(descs, m.Config)

	// Fetch the config blob to populate the cache for later use by Unpack.
	reader, err := repo.FetchBlob(ctx, m.Config)
	if err != nil {
		return nil, fmt.Errorf("fetching config blob: %w", err)
	}
	if err := reader.Close(); err != nil {
		return nil, fmt.Errorf("closing config blob: %w", err)
	}

	descs = append(descs, m.Layers...)
	return descs, nil
}

// LayerFilter inspects or modifies a [tar.Header] during layer extraction.
// Return true to keep the entry, false to skip it. Filters may also mutate
// the header (e.g. to rewrite paths or change ownership). See [CombineFilters],
// [OnlyPaths], [RewritePath], and [AsCurrentUser] for built-in filters.
type LayerFilter = archive.Filter

// ApplyLayers parses manifestBytes as an OCI image manifest, then fetches and
// extracts each layer into dest in order. An optional [LayerFilter] is applied
// to each tar entry during extraction. Layers are automatically decompressed
// (gzip, zstd, etc.) before extraction.
func ApplyLayers(ctx context.Context, repo image.Repository, manifestBytes []byte, dest string, filter LayerFilter) error {
	var m ocispecv1.Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}

	for i, layer := range m.Layers {
		if err := applyLayer(ctx, repo, dest, layer, filter); err != nil {
			return fmt.Errorf("applying layer %d: %w", i, err)
		}
	}
	return nil
}

func applyLayer(ctx context.Context, repo image.Repository, dest string, layer ocispecv1.Descriptor, filter LayerFilter) error {
	reader, err := repo.FetchBlob(ctx, layer)
	if err != nil {
		return err
	}

	decompressed, _, err := compression.AutoDecompress(reader)
	if err != nil {
		return errors.Join(fmt.Errorf("decompressing layer: %w", err), reader.Close())
	}

	var opts []archive.ApplyOpt
	if filter != nil {
		opts = append(opts, archive.WithFilter(filter))
	}
	_, applyErr := archive.Apply(ctx, dest, decompressed, opts...)
	return errors.Join(applyErr, decompressed.Close(), reader.Close())
}

// CombineFilters executes each filter in order. If any filter returns false or an error,
// the combined filter immediately returns that result.
func CombineFilters(filters ...LayerFilter) LayerFilter {
	return func(h *tar.Header) (bool, error) {
		for _, filter := range filters {
			keep, err := filter(h)
			if err != nil {
				return false, err
			}
			if !keep {
				return false, nil
			}
		}
		return true, nil
	}
}

// OnlyPaths returns a [LayerFilter] that keeps only tar entries at or under any
// of the given paths, skipping everything else. Leading slashes are stripped from
// both the filter paths and tar entry names before comparison. Empty strings in
// paths are ignored.
func OnlyPaths(paths ...string) LayerFilter {
	wantPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			wantPaths = append(wantPaths, path.Clean(strings.TrimPrefix(p, "/")))
		}
	}

	return func(h *tar.Header) (bool, error) {
		headerPath := path.Clean(strings.TrimPrefix(h.Name, "/"))
		for _, wantPath := range wantPaths {
			relPath, err := filepath.Rel(wantPath, headerPath)
			if err != nil {
				return false, fmt.Errorf("getting relative path: %w", err)
			}
			if relPath != ".." && !strings.HasPrefix(relPath, "../") {
				return true, nil
			}
		}
		return false, nil
	}
}

// RewritePath returns a filter that rewrites tar entry paths from srcPath to destPath.
// For example, RewritePath("/configs", "/") rewrites "/configs/foo" to "/foo".
// Entries not under srcPath are kept unmodified.
func RewritePath(srcPath, destPath string) LayerFilter {
	cleanSrc := path.Clean(strings.TrimPrefix(srcPath, "/"))
	cleanDest := path.Clean(strings.TrimPrefix(destPath, "/"))

	return func(h *tar.Header) (bool, error) {
		headerPath := path.Clean(strings.TrimPrefix(h.Name, "/"))

		relPath, err := filepath.Rel(cleanSrc, headerPath)
		if err != nil {
			return true, nil
		}
		if relPath == ".." || strings.HasPrefix(relPath, "../") {
			return true, nil
		}

		h.Name = path.Join(cleanDest, relPath)
		return true, nil
	}
}

// AsCurrentUser rewrites tar entry ownership and permissions so that files
// extracted from container images (typically owned by root with restrictive
// modes) are usable by the current non-root process.
func AsCurrentUser() LayerFilter {
	uid := os.Getuid()
	gid := os.Getgid()
	return func(h *tar.Header) (bool, error) {
		h.Uid = uid
		h.Gid = gid
		if h.Typeflag == tar.TypeDir {
			h.Mode |= 0700
		} else {
			h.Mode |= 0600
		}
		h.PAXRecords = nil
		h.Xattrs = nil //nolint:staticcheck
		return true, nil
	}
}
