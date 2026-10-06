package bundle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"
	"helm.sh/helm/v4/pkg/downloader"

	"github.com/operator-framework/library-olm/image/internal/testutil"
)

// buildHelmManifest creates a Helm OCI artifact manifest with the given config,
// chart content blob, and optional extra layers. Returns the manifest descriptor and bytes.
func buildHelmManifest(repo *testutil.FakeRepo, cfg string, chartContent []byte, extraLayers ...ocispecv1.Descriptor) (ocispecv1.Descriptor, []byte) {
	configDesc := repo.AddBlob([]byte(cfg), HelmConfigMediaType)
	chartLayerDesc := repo.AddBlob(chartContent, HelmChartContentMediaType)

	layers := append([]ocispecv1.Descriptor{chartLayerDesc}, extraLayers...)
	manifestBytes := testutil.BuildManifest(configDesc, layers...)
	desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)
	return desc, manifestBytes
}

// buildSignedHelmManifest creates a Helm OCI artifact using the real signtest
// fixtures from Helm's test data. Returns the manifest descriptor and bytes.
func buildSignedHelmManifest(t *testing.T, repo *testutil.FakeRepo) (ocispecv1.Descriptor, []byte) {
	t.Helper()
	chartData, err := os.ReadFile("testdata/signtest-0.1.0.tgz")
	require.NoError(t, err)
	provData, err := os.ReadFile("testdata/signtest-0.1.0.tgz.prov")
	require.NoError(t, err)

	provDesc := repo.AddBlob(provData, HelmProvenanceMediaType)
	return buildHelmManifest(repo,
		`{"name":"signtest","version":"0.1.0"}`,
		chartData,
		provDesc,
	)
}

// --- Tests ---

func TestHelmChartHandler_Name(t *testing.T) {
	h := &HelmChartHandler{}
	assert.Equal(t, "helm.sh/chart", h.Name())
}

func TestHelmChartHandler_Matches(t *testing.T) {
	ctx := context.Background()

	t.Run("HelmConfigMediaType", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		configDesc := repo.AddBlob([]byte(`{"name":"mychart","version":"0.1.0"}`), HelmConfigMediaType)
		manifestBytes := testutil.BuildManifest(configDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("DockerManifest/HelmConfig", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		configDesc := repo.AddBlob([]byte(`{"name":"mychart","version":"0.1.0"}`), HelmConfigMediaType)
		manifestBytes := testutil.BuildManifest(configDesc)
		desc := repo.AddManifest(manifestBytes, manifest.DockerV2Schema2MediaType)

		h := &HelmChartHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("NonHelmConfigMediaType", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			"some-label": "value",
		}, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("UnrecognizedManifestMediaType", func(t *testing.T) {
		h := &HelmChartHandler{}
		desc := ocispecv1.Descriptor{MediaType: "application/vnd.unknown"}
		matched, err := h.Matches(ctx, testutil.NewFakeRepo(), desc, []byte("{}"))
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("MalformedManifest", func(t *testing.T) {
		h := &HelmChartHandler{}
		desc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageManifest}
		matched, err := h.Matches(ctx, testutil.NewFakeRepo(), desc, []byte("not-json"))
		require.Error(t, err)
		assert.False(t, matched)
		assert.Contains(t, err.Error(), "parsing manifest")
	})
}

func TestHelmChartHandler_Discover(t *testing.T) {
	ctx := context.Background()

	t.Run("ReturnsAllDescriptors", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		configDesc := repo.AddBlob([]byte(`{"name":"mychart","version":"0.1.0"}`), HelmConfigMediaType)
		chartLayerDesc := repo.AddBlob([]byte("chart-data"), HelmChartContentMediaType)

		manifestBytes := testutil.BuildManifest(configDesc, chartLayerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{}
		descs, err := h.Discover(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)

		require.Len(t, descs, 3)
		assert.Equal(t, desc.Digest, descs[0].Digest)
		assert.Equal(t, configDesc.Digest, descs[1].Digest)
		assert.Equal(t, chartLayerDesc.Digest, descs[2].Digest)
	})

	t.Run("IncludesProvenanceLayer", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		configDesc := repo.AddBlob([]byte(`{"name":"mychart","version":"0.1.0"}`), HelmConfigMediaType)
		chartLayerDesc := repo.AddBlob([]byte("chart-data"), HelmChartContentMediaType)
		provDesc := repo.AddBlob([]byte("prov-data"), HelmProvenanceMediaType)

		manifestBytes := testutil.BuildManifest(configDesc, chartLayerDesc, provDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{}
		descs, err := h.Discover(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)

		require.Len(t, descs, 4)
	})

	t.Run("InvalidManifest", func(t *testing.T) {
		h := &HelmChartHandler{}
		desc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageManifest}
		_, err := h.Discover(ctx, testutil.NewFakeRepo(), desc, []byte("not-json"))
		require.Error(t, err)
	})
}

func TestHelmChartHandler_Unpack(t *testing.T) {
	ctx := context.Background()

	t.Run("WritesChartPackage", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"mychart","version":"0.1.0"}`,
			[]byte("fake-tgz-content"),
		)

		dest := t.TempDir()
		h := &HelmChartHandler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "mychart-0.1.0.tgz"), "fake-tgz-content")
	})

	t.Run("VerifyNever/SkipsProvenance", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		provDesc := repo.AddBlob([]byte("provenance-data"), HelmProvenanceMediaType)
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"mychart","version":"0.1.0"}`,
			[]byte("chart-data"),
			provDesc,
		)

		dest := t.TempDir()
		h := &HelmChartHandler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "mychart-0.1.0.tgz"), "chart-data")
		testutil.AssertFileNotExists(t, filepath.Join(dest, "mychart-0.1.0.tgz.prov"))
	})

	t.Run("VerifyNever/NoProvenance", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"mychart","version":"0.1.0"}`,
			[]byte("chart-data"),
		)

		dest := t.TempDir()
		h := &HelmChartHandler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileNotExists(t, filepath.Join(dest, "mychart-0.1.0.tgz.prov"))
	})

	t.Run("VerifyAlways/ValidSignature", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := buildSignedHelmManifest(t, repo)

		dest := t.TempDir()
		h := &HelmChartHandler{
			Verify:  downloader.VerifyAlways,
			Keyring: "testdata/helm-test-key.pub",
		}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileExists(t, filepath.Join(dest, "signtest-0.1.0.tgz"))
		testutil.AssertFileExists(t, filepath.Join(dest, "signtest-0.1.0.tgz.prov"))
	})

	t.Run("VerifyAlways/NoProvenance", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"mychart","version":"0.1.0"}`,
			[]byte("chart-data"),
		)

		h := &HelmChartHandler{
			Verify:  downloader.VerifyAlways,
			Keyring: "testdata/helm-test-key.pub",
		}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "provenance data required")
	})

	t.Run("VerifyAlways/InvalidSignature", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		chartData, err := os.ReadFile("testdata/signtest-0.1.0.tgz")
		require.NoError(t, err)

		badProvDesc := repo.AddBlob([]byte("not-a-valid-signature"), HelmProvenanceMediaType)
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"signtest","version":"0.1.0"}`,
			chartData,
			badProvDesc,
		)

		h := &HelmChartHandler{
			Verify:  downloader.VerifyAlways,
			Keyring: "testdata/helm-test-key.pub",
		}
		err = h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verifying chart provenance")
	})

	t.Run("VerifyIfPossible/ValidSignature", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := buildSignedHelmManifest(t, repo)

		dest := t.TempDir()
		h := &HelmChartHandler{
			Verify:  downloader.VerifyIfPossible,
			Keyring: "testdata/helm-test-key.pub",
		}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileExists(t, filepath.Join(dest, "signtest-0.1.0.tgz"))
		testutil.AssertFileExists(t, filepath.Join(dest, "signtest-0.1.0.tgz.prov"))
	})

	t.Run("VerifyIfPossible/NoProvenance", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"mychart","version":"0.1.0"}`,
			[]byte("chart-data"),
		)

		dest := t.TempDir()
		h := &HelmChartHandler{
			Verify:  downloader.VerifyIfPossible,
			Keyring: "testdata/helm-test-key.pub",
		}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "mychart-0.1.0.tgz"), "chart-data")
		testutil.AssertFileNotExists(t, filepath.Join(dest, "mychart-0.1.0.tgz.prov"))
	})

	t.Run("VerifyIfPossible/InvalidSignature", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		chartData, err := os.ReadFile("testdata/signtest-0.1.0.tgz")
		require.NoError(t, err)

		badProvDesc := repo.AddBlob([]byte("not-a-valid-signature"), HelmProvenanceMediaType)
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"signtest","version":"0.1.0"}`,
			chartData,
			badProvDesc,
		)

		h := &HelmChartHandler{
			Verify:  downloader.VerifyIfPossible,
			Keyring: "testdata/helm-test-key.pub",
		}
		err = h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verifying chart provenance")
	})

	t.Run("VerifyLater/WritesProvNoVerification", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		// Use invalid provenance data — VerifyLater should not attempt verification
		badProvDesc := repo.AddBlob([]byte("unverified-prov"), HelmProvenanceMediaType)
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"mychart","version":"0.1.0"}`,
			[]byte("chart-data"),
			badProvDesc,
		)

		dest := t.TempDir()
		h := &HelmChartHandler{Verify: downloader.VerifyLater}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "mychart-0.1.0.tgz"), "chart-data")
		testutil.AssertFileContent(t, filepath.Join(dest, "mychart-0.1.0.tgz.prov"), "unverified-prov")
	})

	t.Run("VerifyLater/NoProvenance", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := buildHelmManifest(repo,
			`{"name":"mychart","version":"0.1.0"}`,
			[]byte("chart-data"),
		)

		h := &HelmChartHandler{Verify: downloader.VerifyLater}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "provenance data required")
	})

	t.Run("NoChartContentLayer", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		configDesc := repo.AddBlob([]byte(`{"name":"mychart","version":"0.1.0"}`), HelmConfigMediaType)
		provLayer := repo.AddBlob([]byte("provenance"), HelmProvenanceMediaType)

		manifestBytes := testutil.BuildManifest(configDesc, provLayer)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), HelmChartContentMediaType)
	})

	t.Run("ChartLayerFetchFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		configDesc := repo.AddBlob([]byte(`{"name":"mychart","version":"0.1.0"}`), HelmConfigMediaType)

		missingLayerDesc := ocispecv1.Descriptor{
			MediaType: HelmChartContentMediaType,
			Digest:    digest.FromString("missing-layer"),
			Size:      1,
		}

		manifestBytes := testutil.BuildManifest(configDesc, missingLayerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing chart package")
	})

	t.Run("ProvenanceFetchFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		chartLayerDesc := repo.AddBlob([]byte("chart-data"), HelmChartContentMediaType)
		configDesc := repo.AddBlob([]byte(`{"name":"mychart","version":"0.1.0"}`), HelmConfigMediaType)

		missingProvDesc := ocispecv1.Descriptor{
			MediaType: HelmProvenanceMediaType,
			Digest:    digest.FromString("missing-prov"),
			Size:      1,
		}

		manifestBytes := testutil.BuildManifest(configDesc, chartLayerDesc, missingProvDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{Verify: downloader.VerifyAlways, Keyring: "testdata/helm-test-key.pub"}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing provenance file")
	})

	t.Run("ConfigFetchFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		missingConfigDesc := ocispecv1.Descriptor{
			MediaType: HelmConfigMediaType,
			Digest:    digest.FromString("missing-config"),
			Size:      1,
		}

		chartLayerDesc := repo.AddBlob([]byte("chart-data"), HelmChartContentMediaType)

		manifestBytes := testutil.BuildManifest(missingConfigDesc, chartLayerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &HelmChartHandler{}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching helm config")
	})

	t.Run("MalformedManifest", func(t *testing.T) {
		h := &HelmChartHandler{}
		desc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageManifest}
		err := h.Unpack(ctx, testutil.NewFakeRepo(), desc, []byte("not-json"), t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing manifest")
	})
}
