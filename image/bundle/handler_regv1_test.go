package bundle

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"

	"github.com/operator-framework/library-olm/image/internal/testutil"
)

// setupUnpackManifest creates a manifest with the given labels and layer files, adds everything
// to the repo, and returns the manifest descriptor and bytes.
func setupUnpackManifest(t *testing.T, repo *testutil.FakeRepo, labels map[string]string, files map[string]string) (ocispecv1.Descriptor, []byte) {
	t.Helper()
	configBlob := testutil.BuildImageConfig(labels)
	configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

	layerData, err := testutil.BuildTarLayer(files)
	require.NoError(t, err)
	layerDesc := repo.AddBlob(layerData, ocispecv1.MediaTypeImageLayerGzip)

	manifestBytes := testutil.BuildManifest(configDesc, layerDesc)
	desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)
	return desc, manifestBytes
}

// --- Tests ---

func TestRegistryV1Handler_Name(t *testing.T) {
	h := &RegistryV1Handler{}
	assert.Equal(t, "olm.operatorframework.io/registry+v1", h.Name())
}

func TestRegistryV1Handler_Matches(t *testing.T) {
	ctx := context.Background()

	t.Run("WithCorrectLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
		}, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("DockerManifest/WithCorrectLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
		}, manifest.DockerV2Schema2MediaType)

		h := &RegistryV1Handler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("WrongLabelValue", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			BundleMediaTypeLabel: "plain+v0",
		}, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("MissingLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			"other-label": "value",
		}, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("NoLabels", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, nil, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("UnrecognizedMediaType", func(t *testing.T) {
		h := &RegistryV1Handler{}
		desc := ocispecv1.Descriptor{MediaType: "application/vnd.unknown"}
		matched, err := h.Matches(ctx, testutil.NewFakeRepo(), desc, []byte("{}"))
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("FetchConfigFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		missingConfigDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageConfig,
			Digest:    digest.FromString("missing"),
			Size:      1,
		}
		manifestBytes := testutil.BuildManifest(missingConfigDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.Error(t, err)
		assert.False(t, matched)
		assert.Contains(t, err.Error(), "fetching image config")
	})
}

func TestRegistryV1Handler_Discover(t *testing.T) {
	ctx := context.Background()

	t.Run("ReturnsAllDescriptors", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		layerDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageLayerGzip,
			Digest:    digest.FromString("layer-data"),
			Size:      100,
		}

		manifestBytes := testutil.BuildManifest(configDesc, layerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		descs, err := h.Discover(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)

		require.Len(t, descs, 3)
		assert.Equal(t, desc.Digest, descs[0].Digest)
		assert.Equal(t, configDesc.Digest, descs[1].Digest)
		assert.Equal(t, layerDesc.Digest, descs[2].Digest)
	})

	t.Run("InvalidManifest", func(t *testing.T) {
		h := &RegistryV1Handler{}
		desc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageManifest}
		_, err := h.Discover(ctx, testutil.NewFakeRepo(), desc, []byte("not-json"))
		require.Error(t, err)
	})
}

func TestRegistryV1Handler_Unpack(t *testing.T) {
	ctx := context.Background()

	t.Run("DefaultDirectories", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := setupUnpackManifest(t, repo, map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
		}, map[string]string{
			"manifests/csv.yaml":          "apiVersion: v1",
			"metadata/annotations.yaml":   "annotations: {}",
			"other/should-not-be-present": "nope",
		})

		dest := t.TempDir()
		h := &RegistryV1Handler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "manifests", "csv.yaml"), "apiVersion: v1")
		testutil.AssertFileContent(t, filepath.Join(dest, "metadata", "annotations.yaml"), "annotations: {}")
		testutil.AssertFileNotExists(t, filepath.Join(dest, "other"))
	})

	t.Run("ManifestsLabelOverridesDefault", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := setupUnpackManifest(t, repo, map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
			BundleManifestsLabel: "/custom-manifests",
		}, map[string]string{
			"custom-manifests/csv.yaml": "custom csv",
			"manifests/csv.yaml":        "default csv",
			"metadata/annotations.yaml": "annotations: {}",
		})

		dest := t.TempDir()
		h := &RegistryV1Handler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "custom-manifests", "csv.yaml"), "custom csv")
		testutil.AssertFileContent(t, filepath.Join(dest, "metadata", "annotations.yaml"), "annotations: {}")
		testutil.AssertFileNotExists(t, filepath.Join(dest, "manifests"))
	})

	t.Run("MetadataLabelOverridesDefault", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := setupUnpackManifest(t, repo, map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
			BundleMetadataLabel:  "/custom-metadata",
		}, map[string]string{
			"manifests/csv.yaml":               "apiVersion: v1",
			"custom-metadata/annotations.yaml": "custom annotations",
			"metadata/annotations.yaml":        "default annotations",
		})

		dest := t.TempDir()
		h := &RegistryV1Handler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "manifests", "csv.yaml"), "apiVersion: v1")
		testutil.AssertFileContent(t, filepath.Join(dest, "custom-metadata", "annotations.yaml"), "custom annotations")
		testutil.AssertFileNotExists(t, filepath.Join(dest, "metadata"))
	})

	t.Run("BothLabelsOverrideDefaults", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := setupUnpackManifest(t, repo, map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
			BundleManifestsLabel: "/my-manifests",
			BundleMetadataLabel:  "/my-metadata",
		}, map[string]string{
			"my-manifests/csv.yaml":        "custom csv",
			"my-metadata/annotations.yaml": "custom annotations",
			"manifests/csv.yaml":           "default csv",
			"metadata/annotations.yaml":    "default annotations",
		})

		dest := t.TempDir()
		h := &RegistryV1Handler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "my-manifests", "csv.yaml"), "custom csv")
		testutil.AssertFileContent(t, filepath.Join(dest, "my-metadata", "annotations.yaml"), "custom annotations")
		testutil.AssertFileNotExists(t, filepath.Join(dest, "manifests"))
		testutil.AssertFileNotExists(t, filepath.Join(dest, "metadata"))
	})

	t.Run("ForcesOwnershipRWX", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := setupUnpackManifest(t, repo, map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
		}, map[string]string{
			"manifests/csv.yaml": "content",
		})

		dest := t.TempDir()
		h := &RegistryV1Handler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		info, err := os.Stat(filepath.Join(dest, "manifests", "csv.yaml"))
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&0700, "owner should have rwx permissions")
	})

	t.Run("FetchConfigFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		missingConfigDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageConfig,
			Digest:    digest.FromString("missing"),
			Size:      1,
		}
		manifestBytes := testutil.BuildManifest(missingConfigDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
	})

	t.Run("LayerFetchFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		configBlob := testutil.BuildImageConfig(map[string]string{
			BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		missingLayerDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageLayerGzip,
			Digest:    digest.FromString("missing-layer"),
			Size:      1,
		}

		manifestBytes := testutil.BuildManifest(configDesc, missingLayerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &RegistryV1Handler{}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
	})
}

func TestRegistryV1Handler_UnpackRejectsSpecialEntries(t *testing.T) {
	for _, tt := range []struct {
		name     string
		path     string
		typeflag byte
		wantErr  string
	}{
		{name: "HardLinkInManifests", path: "manifests/link", typeflag: tar.TypeLink, wantErr: "only regular files and directories are allowed"},
		{name: "SymlinkInManifests", path: "manifests/link", typeflag: tar.TypeSymlink, wantErr: "only regular files and directories are allowed"},
		{name: "DeviceInManifests", path: "manifests/device", typeflag: tar.TypeChar, wantErr: "only regular files and directories are allowed"},
		{name: "HardLinkOutsideSelection", path: "other/link", typeflag: tar.TypeLink},
		{name: "SymlinkOutsideSelection", path: "other/link", typeflag: tar.TypeSymlink},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewFakeRepo()
			layer, err := testutil.BuildTarLayer(nil, tar.Header{
				Name: tt.path, Linkname: "manifests/target", Typeflag: tt.typeflag, Mode: 0644,
			})
			require.NoError(t, err)
			layerDesc := repo.AddBlob(layer, ocispecv1.MediaTypeImageLayer)
			desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
				BundleMediaTypeLabel: BundleMediaTypeRegistryV1,
			}, ocispecv1.MediaTypeImageManifest, layerDesc)

			err = (&RegistryV1Handler{}).Unpack(context.Background(), repo, desc, manifestBytes, t.TempDir())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
