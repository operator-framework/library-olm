package bundle

import (
	"context"
	"fmt"

	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/operator-framework/library-olm/image"
	"github.com/operator-framework/library-olm/image/internal/ociutil"
)

// Bundle label keys used to identify and configure registry+v1 bundles.
const (
	// BundleMediaTypeLabel is the label on bundle images that specifies the bundle format.
	BundleMediaTypeLabel = "operators.operatorframework.io.bundle.mediatype.v1"

	// BundleManifestsLabel is the label on bundle images that specifies the directory
	// containing the bundle manifests (CSV, CRDs, etc.).
	BundleManifestsLabel = "operators.operatorframework.io.bundle.manifests.v1"

	// BundleMetadataLabel is the label on bundle images that specifies the directory
	// containing the bundle metadata (annotations.yaml, properties.yaml).
	BundleMetadataLabel = "operators.operatorframework.io.bundle.metadata.v1"

	// BundleMediaTypeRegistryV1 is the media type value for registry+v1 bundles.
	BundleMediaTypeRegistryV1 = "registry+v1"
)

// RegistryV1Handler is an [image.Handler] that unpacks OLM registry+v1 bundle
// images. It matches images whose config labels include [BundleMediaTypeLabel]
// with value [BundleMediaTypeRegistryV1], then extracts the manifests and
// metadata directories (configured via [BundleManifestsLabel] and
// [BundleMetadataLabel], defaulting to /manifests and /metadata).
type RegistryV1Handler struct{}

func (h *RegistryV1Handler) Name() string { return "olm.operatorframework.io/registry+v1" }

func (h *RegistryV1Handler) Matches(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte) (bool, error) {
	if !image.IsManifest(desc.MediaType) {
		return false, nil
	}

	cfg, err := image.FetchImageConfig(ctx, repo, manifestBytes)
	if err != nil {
		return false, fmt.Errorf("fetching image config: %w", err)
	}

	mediaType, ok := cfg.Config.Labels[BundleMediaTypeLabel]
	return ok && mediaType == BundleMediaTypeRegistryV1, nil
}

func (h *RegistryV1Handler) Discover(ctx context.Context, repo image.Repository, desc ocispecv1.Descriptor, manifestBytes []byte) ([]ocispecv1.Descriptor, error) {
	return ociutil.DiscoverManifestDescriptors(ctx, repo, desc, manifestBytes)
}

func (h *RegistryV1Handler) Unpack(ctx context.Context, repo image.Repository, _ ocispecv1.Descriptor, manifestBytes []byte, dest string) error {
	cfg, err := image.FetchImageConfig(ctx, repo, manifestBytes)
	if err != nil {
		return err
	}

	manifestsDir := "/manifests"
	if v := cfg.Config.Labels[BundleManifestsLabel]; v != "" {
		manifestsDir = v
	}
	metadataDir := "/metadata"
	if v := cfg.Config.Labels[BundleMetadataLabel]; v != "" {
		metadataDir = v
	}

	filter := ociutil.CombineFilters(
		ociutil.OnlyPaths(manifestsDir, metadataDir),
		ociutil.RequireRegularFilesOrDirectories(),
		ociutil.AsCurrentUser(),
	)
	return ociutil.ApplyLayers(ctx, repo, manifestBytes, dest, filter)
}
