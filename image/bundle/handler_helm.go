package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/downloader"

	"github.com/operator-framework/library-olm/image"
	"github.com/operator-framework/library-olm/image/internal/ociutil"
)

// Helm OCI artifact media types as defined by the Helm specification.
// See https://helm.sh/docs/topics/registries/
const (
	// HelmConfigMediaType is the OCI config media type for Helm chart artifacts.
	HelmConfigMediaType = "application/vnd.cncf.helm.config.v1+json"

	// HelmChartContentMediaType is the layer media type containing the chart archive.
	HelmChartContentMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

	// HelmProvenanceMediaType is the layer media type containing the provenance file.
	HelmProvenanceMediaType = "application/vnd.cncf.helm.chart.provenance.v1.prov"
)

// HelmChartHandler is an [image.Handler] that unpacks Helm chart OCI artifacts.
// It matches OCI manifests whose config descriptor has media type
// [HelmConfigMediaType], then writes the chart package and optional provenance
// file into the destination directory.
//
// This produces the inverse of what `helm push` stores:
//   - <name>-<version>.tgz      — the chart package (always written)
//   - <name>-<version>.tgz.prov — the provenance file (when verification is enabled)
//
// Provenance handling is controlled by [HelmChartHandler.Verify]:
//   - [downloader.VerifyNever]:      skip provenance entirely
//   - [downloader.VerifyIfPossible]: write and verify provenance if present, succeed silently if absent
//   - [downloader.VerifyAlways]:     write and verify provenance, error if absent or invalid
//   - [downloader.VerifyLater]:      write provenance (error if absent), skip verification
type HelmChartHandler struct {
	// Verify controls provenance verification behavior. The default
	// (VerifyNever) skips verification entirely.
	Verify downloader.VerificationStrategy

	// Keyring is the path to the PGP keyring file used to verify chart
	// provenance. Required when Verify is VerifyAlways, or when Verify is
	// VerifyIfPossible and a provenance layer is present.
	Keyring string
}

func (h *HelmChartHandler) Name() string { return "helm.sh/chart" }

func (h *HelmChartHandler) Matches(_ context.Context, _ image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte) (bool, error) {
	if !image.IsManifest(desc.MediaType) {
		return false, nil
	}

	var m ocispecv1.Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return false, fmt.Errorf("parsing manifest: %w", err)
	}

	return m.Config.MediaType == HelmConfigMediaType, nil
}

func (h *HelmChartHandler) Discover(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte) ([]ocispecv1.Descriptor, error) {
	return ociutil.DiscoverManifestDescriptors(ctx, repo, desc, manifestBytes)
}

func (h *HelmChartHandler) Unpack(ctx context.Context, repo image.Repository, _ ocispecv1.Descriptor, manifestBytes []byte, dest string) error {
	var m ocispecv1.Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}

	cfg, err := fetchHelmConfig(ctx, repo, m.Config)
	if err != nil {
		return err
	}

	chartLayer, err := findLayer(m.Layers, HelmChartContentMediaType)
	if err != nil {
		return err
	}

	basename := fmt.Sprintf("%s-%s", cfg.Name, cfg.Version)
	tgzPath := filepath.Join(dest, basename+".tgz")

	if err := writeBlob(ctx, repo, chartLayer, tgzPath); err != nil {
		return fmt.Errorf("writing chart package: %w", err)
	}

	provLayer, hasProv := tryFindLayer(m.Layers, HelmProvenanceMediaType)

	switch {
	case h.Verify == downloader.VerifyNever:
		// No provenance handling.
	case !hasProv && h.Verify == downloader.VerifyIfPossible:
		// No provenance available, not required.
	case !hasProv:
		// VerifyAlways or VerifyLater both require the provenance layer.
		return fmt.Errorf("provenance data required but no provenance layer found")
	default:
		// Write the provenance file.
		provPath := filepath.Join(dest, basename+".tgz.prov")
		if err := writeBlob(ctx, repo, provLayer, provPath); err != nil {
			return fmt.Errorf("writing provenance file: %w", err)
		}
		// Verify unless deferred.
		if h.Verify != downloader.VerifyLater {
			if _, err := downloader.VerifyChart(tgzPath, provPath, h.Keyring); err != nil {
				return fmt.Errorf("verifying chart provenance: %w", err)
			}
		}
	}

	return nil
}

func fetchHelmConfig(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor) (*chart.Metadata, error) {
	reader, err := repo.FetchBlob(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("fetching helm config: %w", err)
	}

	var cfg chart.Metadata
	decodeErr := json.NewDecoder(reader).Decode(&cfg)
	if err := errors.Join(decodeErr, reader.Close()); err != nil {
		return nil, fmt.Errorf("reading helm config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating helm config: %w", err)
	}
	return &cfg, nil
}

func writeBlob(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, path string) error {
	reader, err := repo.FetchBlob(ctx, desc)
	if err != nil {
		return err
	}

	data, readErr := io.ReadAll(reader)
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// findLayer returns the first layer descriptor with the given media type.
func findLayer(layers []ocispecv1.Descriptor, mediaType string) (ocispecv1.Descriptor, error) {
	desc, found := tryFindLayer(layers, mediaType)
	if !found {
		return ocispecv1.Descriptor{}, fmt.Errorf("no layer with media type %q found", mediaType)
	}
	return desc, nil
}

// tryFindLayer returns the first layer descriptor with the given media type, if any.
func tryFindLayer(layers []ocispecv1.Descriptor, mediaType string) (ocispecv1.Descriptor, bool) {
	for _, l := range layers {
		if l.MediaType == mediaType {
			return l, true
		}
	}
	return ocispecv1.Descriptor{}, false
}
