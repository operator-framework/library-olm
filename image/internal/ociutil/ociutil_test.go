package ociutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operator-framework/library-olm/image"
	"github.com/operator-framework/library-olm/image/internal/testutil"
)

func TestCombineFilters(t *testing.T) {
	passFilter := func(_ *tar.Header) (bool, error) { return true, nil }
	rejectFilter := func(_ *tar.Header) (bool, error) { return false, nil }
	errorFilter := func(_ *tar.Header) (bool, error) { return false, fmt.Errorf("filter error") }

	t.Run("AllPass", func(t *testing.T) {
		combined := CombineFilters(passFilter, passFilter) //nolint:gocritic // intentional duplicate for testing
		keep, err := combined(&tar.Header{Name: "test"})
		require.NoError(t, err)
		assert.True(t, keep)
	})

	t.Run("FirstRejects", func(t *testing.T) {
		combined := CombineFilters(rejectFilter, passFilter)
		keep, err := combined(&tar.Header{Name: "test"})
		require.NoError(t, err)
		assert.False(t, keep)
	})

	t.Run("SecondRejects", func(t *testing.T) {
		combined := CombineFilters(passFilter, rejectFilter)
		keep, err := combined(&tar.Header{Name: "test"})
		require.NoError(t, err)
		assert.False(t, keep)
	})

	t.Run("ErrorPropagated", func(t *testing.T) {
		combined := CombineFilters(passFilter, errorFilter)
		_, err := combined(&tar.Header{Name: "test"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "filter error")
	})

	t.Run("EmptyFilters", func(t *testing.T) {
		combined := CombineFilters()
		keep, err := combined(&tar.Header{Name: "test"})
		require.NoError(t, err)
		assert.True(t, keep)
	})
}

func TestRequireRegularFilesOrDirectories(t *testing.T) {
	tests := []struct {
		name     string
		typeflag byte
		allowed  bool
	}{
		{name: "RegularFile", typeflag: tar.TypeReg, allowed: true},
		{name: "AlternateRegularFile", typeflag: tar.TypeRegA, allowed: true}, //nolint:staticcheck
		{name: "Directory", typeflag: tar.TypeDir, allowed: true},
		{name: "HardLink", typeflag: tar.TypeLink},
		{name: "Symlink", typeflag: tar.TypeSymlink},
		{name: "CharacterDevice", typeflag: tar.TypeChar},
		{name: "BlockDevice", typeflag: tar.TypeBlock},
		{name: "FIFO", typeflag: tar.TypeFifo},
		{name: "GlobalHeader", typeflag: tar.TypeXGlobalHeader},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keep, err := RequireRegularFilesOrDirectories()(&tar.Header{Name: "entry", Typeflag: tt.typeflag})
			if !tt.allowed {
				assert.False(t, keep)
				require.ErrorContains(t, err, "only regular files and directories are allowed")
				return
			}
			assert.True(t, keep)
			require.NoError(t, err)
		})
	}
}

func TestOnlyPaths(t *testing.T) {
	t.Run("FileUnderWantedPath", func(t *testing.T) {
		filter := OnlyPaths("manifests")
		keep, err := filter(&tar.Header{Name: "manifests/csv.yaml"})
		require.NoError(t, err)
		assert.True(t, keep)
	})

	t.Run("FileNotUnderWantedPath", func(t *testing.T) {
		filter := OnlyPaths("manifests")
		keep, err := filter(&tar.Header{Name: "other/file.yaml"})
		require.NoError(t, err)
		assert.False(t, keep)
	})

	t.Run("LeadingSlashStripped", func(t *testing.T) {
		filter := OnlyPaths("/manifests")
		keep, err := filter(&tar.Header{Name: "manifests/csv.yaml"})
		require.NoError(t, err)
		assert.True(t, keep)
	})
}

func TestRewritePath(t *testing.T) {
	tests := []struct {
		name     string
		srcPath  string
		destPath string
		input    string
		expected string
	}{
		{
			name:     "RewriteUnderSrc",
			srcPath:  "/configs",
			destPath: "/",
			input:    "configs/foo",
			expected: "foo",
		},
		{
			name:     "RewriteNestedPath",
			srcPath:  "/configs",
			destPath: "/",
			input:    "configs/bar/baz",
			expected: "bar/baz",
		},
		{
			name:     "RewriteSrcDirItself",
			srcPath:  "/configs",
			destPath: "/",
			input:    "configs",
			expected: ".",
		},
		{
			name:     "RewriteToDifferentDest",
			srcPath:  "/configs",
			destPath: "/output",
			input:    "configs/foo",
			expected: "output/foo",
		},
		{
			name:     "EntryOutsideSrcUnchanged",
			srcPath:  "/configs",
			destPath: "/",
			input:    "other/file.yaml",
			expected: "other/file.yaml",
		},
		{
			name:     "LeadingSlashOnInput",
			srcPath:  "/configs",
			destPath: "/out",
			input:    "/configs/foo",
			expected: "out/foo",
		},
		{
			name:     "SrcWithoutLeadingSlash",
			srcPath:  "configs",
			destPath: "out",
			input:    "configs/foo",
			expected: "out/foo",
		},
		{
			name:     "SrcPrefixNotDir",
			srcPath:  "/configs",
			destPath: "/",
			input:    "configs-extra/foo",
			expected: "configs-extra/foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := RewritePath(tt.srcPath, tt.destPath)
			h := &tar.Header{Name: tt.input}
			keep, err := filter(h)
			require.NoError(t, err)
			assert.True(t, keep)
			assert.Equal(t, tt.expected, h.Name)
		})
	}
}

func TestAsCurrentUser(t *testing.T) {
	filter := AsCurrentUser()

	t.Run("File", func(t *testing.T) {
		h := &tar.Header{
			Name:       "test",
			Typeflag:   tar.TypeReg,
			Uid:        9999,
			Gid:        9999,
			Mode:       0444,
			PAXRecords: map[string]string{"foo": "bar"},
			Xattrs:     map[string]string{"baz": "qux"}, //nolint:staticcheck
		}
		keep, err := filter(h)
		require.NoError(t, err)
		assert.True(t, keep)

		assert.Equal(t, os.Getuid(), h.Uid)
		assert.Equal(t, os.Getgid(), h.Gid)
		assert.Equal(t, int64(0644), h.Mode, "files get rw, not execute")
		assert.Nil(t, h.PAXRecords)
		assert.Nil(t, h.Xattrs) //nolint:staticcheck
	})

	t.Run("Directory", func(t *testing.T) {
		h := &tar.Header{
			Name:     "testdir/",
			Typeflag: tar.TypeDir,
			Uid:      9999,
			Gid:      9999,
			Mode:     0444,
		}
		keep, err := filter(h)
		require.NoError(t, err)
		assert.True(t, keep)

		assert.Equal(t, os.Getuid(), h.Uid)
		assert.Equal(t, os.Getgid(), h.Gid)
		assert.Equal(t, int64(0744), h.Mode, "directories get rwx for traversal")
	})

	t.Run("FilePreservesExistingExecute", func(t *testing.T) {
		h := &tar.Header{
			Name:     "script.sh",
			Typeflag: tar.TypeReg,
			Mode:     0755,
		}
		keep, err := filter(h)
		require.NoError(t, err)
		assert.True(t, keep)
		assert.Equal(t, int64(0755), h.Mode, "existing execute bit should be preserved")
	})

	t.Run("Symlink", func(t *testing.T) {
		h := &tar.Header{
			Name:     "link",
			Typeflag: tar.TypeSymlink,
			Uid:      9999,
			Gid:      9999,
			Mode:     0444,
		}
		keep, err := filter(h)
		require.NoError(t, err)
		assert.True(t, keep)

		assert.Equal(t, os.Getuid(), h.Uid)
		assert.Equal(t, os.Getgid(), h.Gid)
		assert.Equal(t, int64(0644), h.Mode, "symlinks get file mask, not directory mask")
	})
}

func TestOnlyPaths_MultiplePaths(t *testing.T) {
	filter := OnlyPaths("manifests", "metadata")

	keep, err := filter(&tar.Header{Name: "manifests/csv.yaml"})
	require.NoError(t, err)
	assert.True(t, keep)

	keep, err = filter(&tar.Header{Name: "metadata/annotations.yaml"})
	require.NoError(t, err)
	assert.True(t, keep)

	keep, err = filter(&tar.Header{Name: "other/file.yaml"})
	require.NoError(t, err)
	assert.False(t, keep)
}

func TestOnlyPaths_ExactMatch(t *testing.T) {
	filter := OnlyPaths("manifests")
	keep, err := filter(&tar.Header{Name: "manifests"})
	require.NoError(t, err)
	assert.True(t, keep)
}

func TestOnlyPaths_EmptyPathsIgnored(t *testing.T) {
	filter := OnlyPaths("", "manifests", "")
	keep, err := filter(&tar.Header{Name: "manifests/foo"})
	require.NoError(t, err)
	assert.True(t, keep)

	keep, err = filter(&tar.Header{Name: "other/foo"})
	require.NoError(t, err)
	assert.False(t, keep)
}

func TestOnlyPaths_HeaderLeadingSlash(t *testing.T) {
	filter := OnlyPaths("manifests")
	keep, err := filter(&tar.Header{Name: "/manifests/csv.yaml"})
	require.NoError(t, err)
	assert.True(t, keep)
}

func TestOnlyPaths_NoPaths(t *testing.T) {
	filter := OnlyPaths()
	keep, err := filter(&tar.Header{Name: "anything"})
	require.NoError(t, err)
	assert.False(t, keep)
}

func TestOnlyPaths_AllEmptyPaths(t *testing.T) {
	filter := OnlyPaths("", "")
	keep, err := filter(&tar.Header{Name: "anything"})
	require.NoError(t, err)
	assert.False(t, keep)
}

// makeTarGz creates a gzipped tar archive from a map of filename→content.
func makeTarGz(t *testing.T, uid, gid int, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
			Uid:  uid,
			Gid:  gid,
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestApplyLayers(t *testing.T) {
	ctx := context.Background()
	uid := os.Getuid()
	gid := os.Getgid()

	t.Run("UnpacksLayersInOrder", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		layer1 := makeTarGz(t, uid, gid, map[string]string{"file1.txt": "content1"})
		layer2 := makeTarGz(t, uid, gid, map[string]string{"file2.txt": "content2"})
		desc1 := repo.AddBlob(layer1, "")
		desc2 := repo.AddBlob(layer2, "")

		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Layers: []ocispecv1.Descriptor{desc1, desc2},
		})

		dest := t.TempDir()
		err := ApplyLayers(ctx, repo, manifestBytes, dest, nil)
		require.NoError(t, err)

		got1, err := os.ReadFile(filepath.Join(dest, "file1.txt"))
		require.NoError(t, err)
		assert.Equal(t, "content1", string(got1))

		got2, err := os.ReadFile(filepath.Join(dest, "file2.txt"))
		require.NoError(t, err)
		assert.Equal(t, "content2", string(got2))
	})

	t.Run("LaterLayerOverwritesEarlier", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		layer1 := makeTarGz(t, uid, gid, map[string]string{"file.txt": "old"})
		layer2 := makeTarGz(t, uid, gid, map[string]string{"file.txt": "new"})
		desc1 := repo.AddBlob(layer1, "")
		desc2 := repo.AddBlob(layer2, "")

		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Layers: []ocispecv1.Descriptor{desc1, desc2},
		})

		dest := t.TempDir()
		err := ApplyLayers(ctx, repo, manifestBytes, dest, nil)
		require.NoError(t, err)

		got, err := os.ReadFile(filepath.Join(dest, "file.txt"))
		require.NoError(t, err)
		assert.Equal(t, "new", string(got))
	})

	t.Run("NoLayers", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{})

		dest := t.TempDir()
		err := ApplyLayers(ctx, repo, manifestBytes, dest, nil)
		require.NoError(t, err)
	})

	t.Run("InvalidManifestJSON", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		err := ApplyLayers(ctx, repo, []byte("not json"), t.TempDir(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing manifest")
	})

	t.Run("BlobFetchError", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Layers: []ocispecv1.Descriptor{
				{Digest: digest.FromString("missing")},
			},
		})

		err := ApplyLayers(ctx, repo, manifestBytes, t.TempDir(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "applying layer 0")
	})

	t.Run("BlobReadError", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		layerDesc := ocispecv1.Descriptor{Digest: digest.FromString("bad-layer")}
		repo.AddErrorBlob(layerDesc, fmt.Errorf("disk read failed"))

		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Layers: []ocispecv1.Descriptor{layerDesc},
		})

		err := ApplyLayers(ctx, repo, manifestBytes, t.TempDir(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "applying layer 0")
		assert.Contains(t, err.Error(), "disk read failed")
	})

	t.Run("WithFilter", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		layer := makeTarGz(t, uid, gid, map[string]string{
			"keep.txt": "kept",
			"skip.txt": "skipped",
		})
		desc := repo.AddBlob(layer, "")

		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Layers: []ocispecv1.Descriptor{desc},
		})

		dest := t.TempDir()
		filter := func(h *tar.Header) (bool, error) {
			return h.Name == "keep.txt", nil
		}
		err := ApplyLayers(ctx, repo, manifestBytes, dest, filter)
		require.NoError(t, err)

		_, err = os.Stat(filepath.Join(dest, "keep.txt"))
		require.NoError(t, err)

		_, err = os.Stat(filepath.Join(dest, "skip.txt"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("AsCurrentUserWithRootOwnedTar", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		layer := makeTarGz(t, 0, 0, map[string]string{"root-owned.txt": "hello"})
		desc := repo.AddBlob(layer, "")

		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Layers: []ocispecv1.Descriptor{desc},
		})

		dest := t.TempDir()
		err := ApplyLayers(ctx, repo, manifestBytes, dest, AsCurrentUser())
		require.NoError(t, err)

		got, err := os.ReadFile(filepath.Join(dest, "root-owned.txt"))
		require.NoError(t, err)
		assert.Equal(t, "hello", string(got))

		info, err := os.Stat(filepath.Join(dest, "root-owned.txt"))
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&0600, "owner should have rw")
	})
}

func TestDiscoverManifestDescriptors(t *testing.T) {
	ctx := context.Background()

	t.Run("ReturnsAllDescriptors", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		configBlob := testutil.BuildImageConfig(map[string]string{"label": "value"})
		configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		layerDesc1 := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageLayerGzip,
			Digest:    "sha256:aaa",
			Size:      100,
		}
		layerDesc2 := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageLayerGzip,
			Digest:    "sha256:bbb",
			Size:      200,
		}

		manifestBytes := testutil.BuildManifest(configDesc, layerDesc1, layerDesc2)
		manifestDesc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		descs, err := DiscoverManifestDescriptors(ctx, repo, manifestDesc, manifestBytes)
		require.NoError(t, err)

		// Should return: manifest descriptor, config descriptor, and 2 layer descriptors
		require.Len(t, descs, 4)
		assert.Equal(t, manifestDesc.Digest, descs[0].Digest)
		assert.Equal(t, configDesc.Digest, descs[1].Digest)
		assert.Equal(t, layerDesc1.Digest, descs[2].Digest)
		assert.Equal(t, layerDesc2.Digest, descs[3].Digest)
	})

	t.Run("InvalidManifestJSON", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		desc := ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageManifest}
		_, err := DiscoverManifestDescriptors(ctx, repo, desc, []byte("not json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing manifest")
	})

	t.Run("ConfigFetchError", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		missingConfigDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageConfig,
			Digest:    "sha256:missing",
			Size:      1,
		}
		manifestBytes := testutil.BuildManifest(missingConfigDesc)
		desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		_, err := DiscoverManifestDescriptors(ctx, repo, desc, manifestBytes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching config blob")
	})

	t.Run("CachesConfigForReuse", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		configBlob := testutil.BuildImageConfig(map[string]string{"label": "value"})
		configDesc := inner.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)

		manifestBytes := testutil.BuildManifest(configDesc)
		manifestDesc := inner.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

		cached, err := image.NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cached.Close() })

		_, err = DiscoverManifestDescriptors(ctx, cached, manifestDesc, manifestBytes)
		require.NoError(t, err)

		// Config should be in cache now — fetching again should not hit inner
		reader, err := cached.FetchBlob(ctx, configDesc)
		require.NoError(t, err)
		_ = reader.Close()

		assert.Equal(t, int32(1), inner.FetchBlobCount.Load(), "config should be fetched only once")
	})
}
