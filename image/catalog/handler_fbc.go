package catalog

import (
	"context"
	"fmt"
	"runtime"

	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/operator-framework/library-olm/image"
	"github.com/operator-framework/library-olm/image/internal/ociutil"
)

// ConfigDirLabel is the label on catalog images that specifies the directory
// containing the catalog configuration.
const ConfigDirLabel = "operators.operatorframework.io.index.configs.v1"

// FBCHandler is an [image.Handler] that unpacks file-based catalog (FBC) images.
// It matches images whose config labels include [ConfigDirLabel], then extracts
// only that directory, rewriting its contents to the root of the destination.
//
// FBCHandler supports both single-platform manifests and multi-platform manifest
// lists/indexes. For multi-platform images, it selects the linux/GOARCH variant.
type FBCHandler struct{}

func (h *FBCHandler) Name() string { return "olm.operatorframework.io/fbc+v0" }

func (h *FBCHandler) Matches(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte) (bool, error) {
	// If this is a manifest list/index, resolve to the platform-specific manifest first
	if image.IsIndex(desc.MediaType) {
		platformDesc, platformManifestBytes, err := resolvePlatformManifest(ctx, repo, manifestBytes, desc.MediaType)
		if err != nil {
			return false, fmt.Errorf("resolving platform manifest: %w", err)
		}
		desc = platformDesc
		manifestBytes = platformManifestBytes
	}

	if !image.IsManifest(desc.MediaType) {
		return false, nil
	}

	cfg, err := image.FetchImageConfig(ctx, repo, manifestBytes)
	if err != nil {
		return false, fmt.Errorf("fetching image config: %w", err)
	}

	configDir, ok := cfg.Config.Labels[ConfigDirLabel]
	return ok && configDir != "", nil
}

func (h *FBCHandler) Discover(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte) ([]ocispecv1.Descriptor, error) {
	if image.IsIndex(desc.MediaType) {
		platformDesc, platformManifestBytes, err := resolvePlatformManifest(ctx, repo, manifestBytes, desc.MediaType)
		if err != nil {
			return nil, fmt.Errorf("resolving platform manifest: %w", err)
		}
		desc = platformDesc
		manifestBytes = platformManifestBytes
	}

	return ociutil.DiscoverManifestDescriptors(ctx, repo, desc, manifestBytes)
}

func (h *FBCHandler) Unpack(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte, dest string) error {
	// If this is a manifest list/index, resolve to the platform-specific manifest first
	if image.IsIndex(desc.MediaType) {
		platformDesc, platformManifestBytes, err := resolvePlatformManifest(ctx, repo, manifestBytes, desc.MediaType)
		if err != nil {
			return fmt.Errorf("resolving platform manifest: %w", err)
		}
		desc = platformDesc
		manifestBytes = platformManifestBytes
	}

	cfg, err := image.FetchImageConfig(ctx, repo, manifestBytes)
	if err != nil {
		return fmt.Errorf("fetching image config: %w", err)
	}

	configDir, ok := cfg.Config.Labels[ConfigDirLabel]
	if !ok || configDir == "" {
		return fmt.Errorf("image config is missing a non-empty %q label", ConfigDirLabel)
	}

	filter := ociutil.CombineFilters(
		ociutil.OnlyPaths(configDir),
		ociutil.RewritePath(configDir, "/"),
		ociutil.AsCurrentUser(),
	)
	return ociutil.ApplyLayers(ctx, repo, manifestBytes, dest, filter)
}

// resolvePlatformManifest selects the appropriate platform manifest from a manifest list/index.
func resolvePlatformManifest(ctx context.Context, repo image.Repository, indexBytes []byte, indexMediaType string) (ocispecv1.Descriptor, []byte, error) {
	list, err := manifest.ListFromBlob(indexBytes, indexMediaType)
	if err != nil {
		return ocispecv1.Descriptor{}, nil, fmt.Errorf("parsing manifest list: %w", err)
	}

	// Use linux as the OS since catalog images are linux container images.
	// The architecture is inherited from the current runtime.
	chosenDigest, err := list.ChooseInstance(&types.SystemContext{
		OSChoice:           "linux",
		ArchitectureChoice: runtime.GOARCH,
	})
	if err != nil {
		return ocispecv1.Descriptor{}, nil, fmt.Errorf("choosing platform instance: %w", err)
	}

	desc := ocispecv1.Descriptor{Digest: chosenDigest}
	manifestBytes, mediaType, err := repo.FetchManifest(ctx, desc)
	if err != nil {
		return ocispecv1.Descriptor{}, nil, fmt.Errorf("fetching platform manifest: %w", err)
	}
	desc.MediaType = mediaType
	desc.Size = int64(len(manifestBytes))

	return desc, manifestBytes, nil
}
