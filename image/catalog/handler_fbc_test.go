package catalog

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"

	"github.com/operator-framework/library-olm/image/internal/testutil"
)

func buildDockerManifestList(entries ...manifestListEntry) []byte {
	type platformSpec struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	}
	type entry struct {
		MediaType string       `json:"mediaType"`
		Size      int64        `json:"size"`
		Digest    string       `json:"digest"`
		Platform  platformSpec `json:"platform"`
	}
	type list struct {
		SchemaVersion int     `json:"schemaVersion"`
		MediaType     string  `json:"mediaType"`
		Manifests     []entry `json:"manifests"`
	}

	ml := list{
		SchemaVersion: 2,
		MediaType:     manifest.DockerV2ListMediaType,
	}
	for _, e := range entries {
		ml.Manifests = append(ml.Manifests, entry{
			MediaType: e.desc.MediaType,
			Size:      e.desc.Size,
			Digest:    e.desc.Digest.String(),
			Platform: platformSpec{
				Architecture: e.arch,
				OS:           e.os,
			},
		})
	}
	return testutil.MustJSON(ml)
}

func buildOCIIndex(entries ...manifestListEntry) []byte {
	idx := ocispecv1.Index{
		MediaType: ocispecv1.MediaTypeImageIndex,
		Manifests: make([]ocispecv1.Descriptor, 0, len(entries)),
	}
	for _, e := range entries {
		d := e.desc
		d.Platform = &ocispecv1.Platform{
			Architecture: e.arch,
			OS:           e.os,
		}
		idx.Manifests = append(idx.Manifests, d)
	}
	return testutil.MustJSON(idx)
}

type manifestListEntry struct {
	desc ocispecv1.Descriptor
	arch string
	os   string
}

// --- Tests ---

func TestFBCHandler_Name(t *testing.T) {
	h := &FBCHandler{}
	assert.Equal(t, "olm.operatorframework.io/fbc+v0", h.Name())
}

func TestFBCHandler_Matches(t *testing.T) {
	ctx := context.Background()

	t.Run("SingleManifest/WithLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			ConfigDirLabel: "/configs",
		}, ocispecv1.MediaTypeImageManifest)

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("SingleManifest/WithoutLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			"other-label": "value",
		}, ocispecv1.MediaTypeImageManifest)

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("SingleManifest/EmptyLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			ConfigDirLabel: "",
		}, ocispecv1.MediaTypeImageManifest)

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("SingleManifest/NoLabels", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, nil, ocispecv1.MediaTypeImageManifest)

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("DockerManifest/WithLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
			ConfigDirLabel: "/configs",
		}, manifest.DockerV2Schema2MediaType)

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("DockerManifest/WithoutLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{}, manifest.DockerV2Schema2MediaType)

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("UnrecognizedMediaType", func(t *testing.T) {
		h := &FBCHandler{}
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

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, desc, manifestBytes)
		require.Error(t, err)
		assert.False(t, matched)
		assert.Contains(t, err.Error(), "fetching image config")
	})

	t.Run("OCIIndex/WithLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		platformDesc, _ := testutil.SetupSingleManifest(repo, map[string]string{
			ConfigDirLabel: "/configs",
		}, ocispecv1.MediaTypeImageManifest)

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: platformDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})
		indexDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageIndex,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, indexDesc, indexBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("OCIIndex/WithoutLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		platformDesc, _ := testutil.SetupSingleManifest(repo, map[string]string{}, ocispecv1.MediaTypeImageManifest)

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: platformDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})
		indexDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageIndex,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, indexDesc, indexBytes)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("DockerManifestList/WithLabel", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		platformDesc, _ := testutil.SetupSingleManifest(repo, map[string]string{
			ConfigDirLabel: "/configs",
		}, manifest.DockerV2Schema2MediaType)

		indexBytes := buildDockerManifestList(manifestListEntry{
			desc: platformDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})
		indexDesc := ocispecv1.Descriptor{
			MediaType: manifest.DockerV2ListMediaType,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, indexDesc, indexBytes)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("Index/NoPlatformMatch", func(t *testing.T) {
		if runtime.GOARCH == "s390x" {
			t.Skip("test requires non-s390x architecture")
		}
		repo := testutil.NewFakeRepo()
		platformDesc, _ := testutil.SetupSingleManifest(repo, map[string]string{
			ConfigDirLabel: "/configs",
		}, ocispecv1.MediaTypeImageManifest)

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: platformDesc,
			arch: "s390x",
			os:   "linux",
		})
		indexDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageIndex,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, indexDesc, indexBytes)
		require.Error(t, err)
		assert.False(t, matched)
		assert.Contains(t, err.Error(), "resolving platform manifest")
	})

	t.Run("Index/ManifestFetchFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		missingDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Digest:    digest.FromString("missing-manifest"),
			Size:      1,
		}

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: missingDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})
		indexDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageIndex,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}

		h := &FBCHandler{}
		matched, err := h.Matches(ctx, repo, indexDesc, indexBytes)
		require.Error(t, err)
		assert.False(t, matched)
		assert.Contains(t, err.Error(), "resolving platform manifest")
	})

	t.Run("Index/MalformedIndexBytes", func(t *testing.T) {
		h := &FBCHandler{}
		desc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageIndex}
		matched, err := h.Matches(ctx, testutil.NewFakeRepo(), desc, []byte("not-json"))
		require.Error(t, err)
		assert.False(t, matched)
		assert.Contains(t, err.Error(), "resolving platform manifest")
	})
}

func TestFBCHandler_Unpack(t *testing.T) {
	ctx := context.Background()

	t.Run("ExtractsOnlyConfigDir", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{
			ConfigDirLabel: "/configs",
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		layerData, err := testutil.BuildTarLayer(map[string]string{
			"configs/package.json":          `{"name":"test"}`,
			"configs/subdir/operator.yaml":  "apiVersion: v1",
			"other/should-not-be-extracted": "nope",
		})
		require.NoError(t, err)
		layerDesc := repo.AddBlob(layerData, ocispecv1.MediaTypeImageLayerGzip)

		manifestBytes := testutil.BuildManifest(configDesc, layerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		dest := t.TempDir()
		h := &FBCHandler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		// Config dir contents should be at the root of dest (path rewrite)
		testutil.AssertFileExists(t, filepath.Join(dest, "package.json"))
		testutil.AssertFileContent(t, filepath.Join(dest, "package.json"), `{"name":"test"}`)
		testutil.AssertFileExists(t, filepath.Join(dest, "subdir", "operator.yaml"))
		testutil.AssertFileContent(t, filepath.Join(dest, "subdir", "operator.yaml"), "apiVersion: v1")

		// Files outside config dir should not exist
		testutil.AssertFileNotExists(t, filepath.Join(dest, "other"))
		testutil.AssertFileNotExists(t, filepath.Join(dest, "configs"))
	})

	t.Run("ForcesOwnershipRWX", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{
			ConfigDirLabel: "/configs",
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		layerData, err := testutil.BuildTarLayer(map[string]string{
			"configs/file.txt": "content",
		})
		require.NoError(t, err)
		layerDesc := repo.AddBlob(layerData, ocispecv1.MediaTypeImageLayerGzip)

		manifestBytes := testutil.BuildManifest(configDesc, layerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		dest := t.TempDir()
		h := &FBCHandler{}
		require.NoError(t, h.Unpack(ctx, repo, desc, manifestBytes, dest))

		info, err := os.Stat(filepath.Join(dest, "file.txt"))
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

		h := &FBCHandler{}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching image config")
	})

	t.Run("RejectsMissingOrEmptyConfigDirLabel", func(t *testing.T) {
		tests := []struct {
			name   string
			labels map[string]string
		}{
			{name: "missing label", labels: map[string]string{"other-label": "value"}},
			{name: "empty label", labels: map[string]string{ConfigDirLabel: ""}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				repo := testutil.NewFakeRepo()
				configBlob := testutil.BuildImageConfig(tt.labels)
				configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)
				manifestBytes := testutil.BuildManifest(configDesc)
				desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

				err := (&FBCHandler{}).Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
				require.ErrorContains(t, err, "missing a non-empty")
				assert.Contains(t, err.Error(), ConfigDirLabel)
			})
		}
	})

	t.Run("LayerFetchFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{
			ConfigDirLabel: "/configs",
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		missingLayerDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageLayerGzip,
			Digest:    digest.FromString("missing-layer"),
			Size:      1,
		}

		manifestBytes := testutil.BuildManifest(configDesc, missingLayerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &FBCHandler{}
		err := h.Unpack(ctx, repo, desc, manifestBytes, t.TempDir())
		require.Error(t, err)
	})

	t.Run("Index/ResolvesAndUnpacks", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{
			ConfigDirLabel: "/configs",
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		layerData, err := testutil.BuildTarLayer(map[string]string{
			"configs/catalog.json": `{"catalog":true}`,
		})
		require.NoError(t, err)
		layerDesc := repo.AddBlob(layerData, ocispecv1.MediaTypeImageLayerGzip)

		manifestBytes := testutil.BuildManifest(configDesc, layerDesc)
		platformDesc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: platformDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})
		indexDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageIndex,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}

		dest := t.TempDir()
		h := &FBCHandler{}
		require.NoError(t, h.Unpack(ctx, repo, indexDesc, indexBytes, dest))

		testutil.AssertFileContent(t, filepath.Join(dest, "catalog.json"), `{"catalog":true}`)
	})

	t.Run("Index/ResolutionFails", func(t *testing.T) {
		h := &FBCHandler{}
		indexDesc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageIndex}
		err := h.Unpack(ctx, testutil.NewFakeRepo(), indexDesc, []byte("not-json"), t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving platform manifest")
	})
}

func TestFBCHandler_UnpackRejectsSpecialEntries(t *testing.T) {
	for _, tt := range []struct {
		name     string
		path     string
		typeflag byte
		wantErr  string
	}{
		{name: "HardLinkInConfigs", path: "configs/link", typeflag: tar.TypeLink, wantErr: "only regular files and directories are allowed"},
		{name: "SymlinkInConfigs", path: "configs/link", typeflag: tar.TypeSymlink, wantErr: "only regular files and directories are allowed"},
		{name: "FIFOInConfigs", path: "configs/pipe", typeflag: tar.TypeFifo, wantErr: "only regular files and directories are allowed"},
		{name: "HardLinkOutsideSelection", path: "other/link", typeflag: tar.TypeLink},
		{name: "SymlinkOutsideSelection", path: "other/link", typeflag: tar.TypeSymlink},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewFakeRepo()
			layer, err := testutil.BuildTarLayer(nil, tar.Header{
				Name: tt.path, Linkname: "configs/target", Typeflag: tt.typeflag, Mode: 0644,
			})
			require.NoError(t, err)
			layerDesc := repo.AddBlob(layer, ocispecv1.MediaTypeImageLayer)
			desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
				ConfigDirLabel: "/configs",
			}, ocispecv1.MediaTypeImageManifest, layerDesc)

			err = (&FBCHandler{}).Unpack(context.Background(), repo, desc, manifestBytes, t.TempDir())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFBCHandler_Discover(t *testing.T) {
	ctx := context.Background()

	t.Run("SingleManifest", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{
			ConfigDirLabel: "/configs",
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		layerDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageLayerGzip,
			Digest:    digest.FromString("layer-content"),
			Size:      100,
		}

		manifestBytes := testutil.BuildManifest(configDesc, layerDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		h := &FBCHandler{}
		descs, err := h.Discover(ctx, repo, desc, manifestBytes)
		require.NoError(t, err)

		assert.Len(t, descs, 3)
		assert.Equal(t, desc.Digest, descs[0].Digest)
		assert.Equal(t, configDesc.Digest, descs[1].Digest)
		assert.Equal(t, layerDesc.Digest, descs[2].Digest)
	})

	t.Run("IndexResolvesToPlatform", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{
			ConfigDirLabel: "/configs",
		})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		layerDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageLayerGzip,
			Digest:    digest.FromString("layer"),
			Size:      50,
		}

		manifestBytes := testutil.BuildManifest(configDesc, layerDesc)
		platformDesc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: platformDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})
		indexDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageIndex,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}

		h := &FBCHandler{}
		descs, err := h.Discover(ctx, repo, indexDesc, indexBytes)
		require.NoError(t, err)

		assert.Len(t, descs, 3)
		assert.Equal(t, platformDesc.Digest, descs[0].Digest)
	})

	t.Run("IndexResolutionFails", func(t *testing.T) {
		h := &FBCHandler{}
		indexDesc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageIndex}
		_, err := h.Discover(ctx, testutil.NewFakeRepo(), indexDesc, []byte("not-json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving platform manifest")
	})
}

func TestResolvePlatformManifest(t *testing.T) {
	ctx := context.Background()

	t.Run("SelectsCurrentArchitecture", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		currentArchManifest := []byte(`{"current":"yes"}`)
		currentDesc := repo.AddManifest(currentArchManifest, ocispecv1.MediaTypeImageManifest)

		otherArchManifest := []byte(`{"current":"no"}`)
		otherDesc := repo.AddManifest(otherArchManifest, ocispecv1.MediaTypeImageManifest)

		indexBytes := buildOCIIndex(
			manifestListEntry{desc: otherDesc, arch: "s390x", os: "linux"},
			manifestListEntry{desc: currentDesc, arch: runtime.GOARCH, os: "linux"},
		)

		desc, manifestBytes, err := resolvePlatformManifest(ctx, repo, indexBytes, ocispecv1.MediaTypeImageIndex)
		require.NoError(t, err)
		assert.Equal(t, currentDesc.Digest, desc.Digest)
		assert.Equal(t, currentArchManifest, manifestBytes)
	})

	t.Run("MalformedIndexBytes", func(t *testing.T) {
		_, _, err := resolvePlatformManifest(ctx, testutil.NewFakeRepo(), []byte("garbage"), ocispecv1.MediaTypeImageIndex)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing manifest list")
	})

	t.Run("NoMatchingPlatform", func(t *testing.T) {
		if runtime.GOARCH == "s390x" {
			t.Skip("test requires non-s390x architecture")
		}
		repo := testutil.NewFakeRepo()

		otherManifest := []byte(`{"other":true}`)
		otherDesc := repo.AddManifest(otherManifest, ocispecv1.MediaTypeImageManifest)

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: otherDesc,
			arch: "s390x",
			os:   "linux",
		})

		_, _, err := resolvePlatformManifest(ctx, repo, indexBytes, ocispecv1.MediaTypeImageIndex)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "choosing platform instance")
	})

	t.Run("FetchManifestFails", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		missingDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Digest:    digest.FromString("not-in-repo"),
			Size:      1,
		}

		indexBytes := buildOCIIndex(manifestListEntry{
			desc: missingDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})

		_, _, err := resolvePlatformManifest(ctx, repo, indexBytes, ocispecv1.MediaTypeImageIndex)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching platform manifest")
	})

	t.Run("DockerManifestList", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		platformManifest := []byte(`{"docker":"manifest"}`)
		platformDesc := repo.AddManifest(platformManifest, manifest.DockerV2Schema2MediaType)

		indexBytes := buildDockerManifestList(manifestListEntry{
			desc: platformDesc,
			arch: runtime.GOARCH,
			os:   "linux",
		})

		desc, manifestBytes, err := resolvePlatformManifest(ctx, repo, indexBytes, manifest.DockerV2ListMediaType)
		require.NoError(t, err)
		assert.Equal(t, platformDesc.Digest, desc.Digest)
		assert.Equal(t, platformManifest, manifestBytes)
	})
}
