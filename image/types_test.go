package image

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"

	"github.com/operator-framework/library-olm/image/internal/testutil"
)

func TestIsIndex(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		want      bool
	}{
		{"OCIIndex", ocispecv1.MediaTypeImageIndex, true},
		{"DockerManifestList", manifest.DockerV2ListMediaType, true},
		{"OCIManifest", ocispecv1.MediaTypeImageManifest, false},
		{"Empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsIndex(tt.mediaType))
		})
	}
}

func TestIsManifest(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		want      bool
	}{
		{"OCIManifest", ocispecv1.MediaTypeImageManifest, true},
		{"DockerSchema2", manifest.DockerV2Schema2MediaType, true},
		{"OCIIndex", ocispecv1.MediaTypeImageIndex, false},
		{"Empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsManifest(tt.mediaType))
		})
	}
}

func TestNewCachingRepository(t *testing.T) {
	inner := testutil.NewFakeRepo()
	repo, err := NewCachingRepository(inner)
	require.NoError(t, err)
	t.Cleanup(func() { _ = repo.Close() })

	assert.NotEmpty(t, repo.cacheDir)
	_, err = os.Stat(repo.cacheDir)
	assert.NoError(t, err, "cache dir should exist")
}

func TestCachingRepository_Named(t *testing.T) {
	inner := testutil.NewFakeRepo()
	repo, err := NewCachingRepository(inner)
	require.NoError(t, err)
	t.Cleanup(func() { _ = repo.Close() })

	assert.Equal(t, inner.Named().String(), repo.Named().String())
}

func TestCachingRepository_Resolve(t *testing.T) {
	ctx := context.Background()

	t.Run("DelegatesToInner", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		inner.ResolveDesc = ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Digest:    digest.FromString("test"),
			Size:      42,
		}
		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		desc, err := repo.Resolve(ctx)
		require.NoError(t, err)
		assert.Equal(t, inner.ResolveDesc, desc)
		assert.Equal(t, int32(1), inner.ResolveCount.Load())
	})

	t.Run("CachesResult", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		inner.ResolveDesc = ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Digest:    digest.FromString("test"),
			Size:      42,
		}
		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		desc1, err := repo.Resolve(ctx)
		require.NoError(t, err)
		desc2, err := repo.Resolve(ctx)
		require.NoError(t, err)

		assert.Equal(t, desc1, desc2)
		assert.Equal(t, int32(1), inner.ResolveCount.Load(), "inner should only be called once")
	})

	t.Run("PropagatesError", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		inner.ResolveErr = fmt.Errorf("resolve failed")
		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		_, err = repo.Resolve(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolve failed")

		// Error should not be cached — next call should try again
		assert.Nil(t, repo.resolution.Load())
	})

	t.Run("DeduplicatesConcurrentCalls", func(t *testing.T) {
		inner := newBlockingFakeRepo()
		inner.ResolveDesc = ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Digest:    digest.FromString("test"),
			Size:      42,
		}
		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		const n = 10
		var wg sync.WaitGroup
		results := make([]ocispecv1.Descriptor, n)
		errs := make([]error, n)

		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = repo.Resolve(ctx)
			}()
		}

		close(inner.gate)
		wg.Wait()

		for i := range n {
			require.NoError(t, errs[i])
			assert.Equal(t, inner.ResolveDesc, results[i])
		}
		assert.Equal(t, int32(1), inner.ResolveCount.Load(), "inner should be called exactly once")
	})

	t.Run("SingleflightInnerRecheckHitsCache", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		expectedDesc := ocispecv1.Descriptor{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Digest:    digest.FromString("test"),
			Size:      42,
		}

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		// Pre-populate the cache as if a previous singleflight completed
		repo.resolution.Store(&expectedDesc)

		// Call fetchAndCacheResolution directly — the inner re-check should
		// find the cached entry and return without calling the inner repo.
		desc, err := repo.fetchAndCacheResolution(ctx)
		require.NoError(t, err)
		assert.Equal(t, expectedDesc, desc)
		assert.Equal(t, int32(0), inner.ResolveCount.Load(), "inner should not be called")
	})
}

func TestCachingRepository_FetchManifest(t *testing.T) {
	ctx := context.Background()

	// Build a valid OCI manifest so GuessMIMEType can identify it on cache hit.
	validManifest := testutil.MustJSON(ocispecv1.Manifest{
		MediaType: ocispecv1.MediaTypeImageManifest,
		Config:    ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
	})

	t.Run("DelegatesToInnerAndCaches", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		desc := inner.AddManifest(validManifest, ocispecv1.MediaTypeImageManifest)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		got, mediaType, err := repo.FetchManifest(ctx, desc)
		require.NoError(t, err)
		assert.Equal(t, validManifest, got)
		assert.Equal(t, ocispecv1.MediaTypeImageManifest, mediaType)
		assert.Equal(t, int32(1), inner.FetchManifestCount.Load())
	})

	t.Run("SecondCallFromCache", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		desc := inner.AddManifest(validManifest, ocispecv1.MediaTypeImageManifest)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		got1, _, err := repo.FetchManifest(ctx, desc)
		require.NoError(t, err)
		got2, _, err := repo.FetchManifest(ctx, desc)
		require.NoError(t, err)

		assert.Equal(t, got1, got2)
		assert.Equal(t, int32(1), inner.FetchManifestCount.Load(), "inner should only be called once")
	})

	t.Run("PropagatesError", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		missingDesc := ocispecv1.Descriptor{
			Digest: digest.FromString("missing"),
		}

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		_, _, err = repo.FetchManifest(ctx, missingDesc)
		require.Error(t, err)
	})

	t.Run("CachedMediaTypePreservesOriginal", func(t *testing.T) {
		manifestWithCustomConfig := testutil.MustJSON(struct {
			SchemaVersion int                  `json:"schemaVersion"`
			Config        ocispecv1.Descriptor `json:"config"`
		}{
			SchemaVersion: 2,
			Config: ocispecv1.Descriptor{
				MediaType: "application/vnd.custom.config.v1+json",
				Digest:    digest.FromString("cfg"),
			},
		})

		inner := testutil.NewFakeRepo()
		desc := inner.AddManifest(manifestWithCustomConfig, manifest.DockerV2Schema2MediaType)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		_, mt1, err := repo.FetchManifest(ctx, desc)
		require.NoError(t, err)
		assert.Equal(t, manifest.DockerV2Schema2MediaType, mt1)

		_, mt2, err := repo.FetchManifest(ctx, desc)
		require.NoError(t, err)
		assert.Equal(t, mt1, mt2, "cached media type should match original media type")
	})

	t.Run("DeduplicatesConcurrentCalls", func(t *testing.T) {
		inner := newBlockingFakeRepo()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Config:    ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		desc := inner.AddManifest(manifestData, ocispecv1.MediaTypeImageManifest)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		const n = 10
		var wg sync.WaitGroup
		results := make([][]byte, n)
		mediaTypes := make([]string, n)
		errs := make([]error, n)

		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], mediaTypes[i], errs[i] = repo.FetchManifest(ctx, desc)
			}()
		}

		close(inner.gate)
		wg.Wait()

		for i := range n {
			require.NoError(t, errs[i])
			assert.Equal(t, manifestData, results[i])
			assert.Equal(t, ocispecv1.MediaTypeImageManifest, mediaTypes[i])
		}
		assert.Equal(t, int32(1), inner.FetchManifestCount.Load(), "inner should be called exactly once")
	})

	t.Run("SingleflightInnerRecheckHitsCache", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			MediaType: ocispecv1.MediaTypeImageManifest,
			Config:    ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		desc := inner.AddManifest(manifestData, ocispecv1.MediaTypeImageManifest)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		// Pre-populate the cache as if a previous singleflight completed
		digestKey := desc.Digest.String()
		repo.manifests.Store(digestKey, &cachedManifest{
			bytes:     manifestData,
			mediaType: ocispecv1.MediaTypeImageManifest,
		})

		// Call fetchAndCacheManifest directly — the inner re-check should
		// find the cached entry and return without calling the inner repo.
		result, err := repo.fetchAndCacheManifest(ctx, desc, digestKey)
		require.NoError(t, err)
		assert.Equal(t, manifestData, result.bytes)
		assert.Equal(t, ocispecv1.MediaTypeImageManifest, result.mediaType)
		assert.Equal(t, int32(0), inner.FetchManifestCount.Load(), "inner should not be called")
	})
}

// blockingFakeRepo wraps a FakeRepo, blocking Resolve, FetchManifest, and
// FetchBlob until a channel is closed. This allows tests to control timing
// for concurrent singleflight scenarios.
type blockingFakeRepo struct {
	*testutil.FakeRepo
	gate chan struct{}
}

func newBlockingFakeRepo() *blockingFakeRepo {
	return &blockingFakeRepo{
		FakeRepo: testutil.NewFakeRepo(),
		gate:     make(chan struct{}),
	}
}

func (r *blockingFakeRepo) Resolve(ctx context.Context) (ocispecv1.Descriptor, error) {
	<-r.gate
	return r.FakeRepo.Resolve(ctx)
}

func (r *blockingFakeRepo) FetchBlob(ctx context.Context, desc ocispecv1.Descriptor) (io.ReadCloser, error) {
	<-r.gate
	return r.FakeRepo.FetchBlob(ctx, desc)
}

func (r *blockingFakeRepo) FetchManifest(ctx context.Context, desc ocispecv1.Descriptor) ([]byte, string, error) {
	<-r.gate
	return r.FakeRepo.FetchManifest(ctx, desc)
}

func TestCachingRepository_FetchBlob(t *testing.T) {
	ctx := context.Background()

	t.Run("DelegatesToInnerAndCaches", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		blobData := []byte("hello blob")
		desc := inner.AddBlob(blobData, "")

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		reader, err := repo.FetchBlob(ctx, desc)
		require.NoError(t, err)
		got, err := io.ReadAll(reader)
		require.NoError(t, reader.Close())
		require.NoError(t, err)

		assert.Equal(t, blobData, got)
		assert.Equal(t, int32(1), inner.FetchBlobCount.Load())
	})

	t.Run("SecondCallFromCache", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		blobData := []byte("hello blob")
		desc := inner.AddBlob(blobData, "")

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		reader1, err := repo.FetchBlob(ctx, desc)
		require.NoError(t, err)
		got1, _ := io.ReadAll(reader1)
		_ = reader1.Close()

		reader2, err := repo.FetchBlob(ctx, desc)
		require.NoError(t, err)
		got2, _ := io.ReadAll(reader2)
		_ = reader2.Close()

		assert.Equal(t, got1, got2)
		assert.Equal(t, int32(1), inner.FetchBlobCount.Load(), "inner should only be called once")
	})

	t.Run("PropagatesError", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		missingDesc := ocispecv1.Descriptor{
			Digest: digest.FromString("missing"),
		}

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		_, err = repo.FetchBlob(ctx, missingDesc)
		require.Error(t, err)
	})

	t.Run("ClosesInnerReader", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		blobData := []byte("hello blob")
		desc := inner.AddBlob(blobData, "")

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		reader, err := repo.FetchBlob(ctx, desc)
		require.NoError(t, err)
		_, _ = io.ReadAll(reader)
		require.NoError(t, reader.Close())

		// Verify the blob was cached (inner not called again)
		reader2, err := repo.FetchBlob(ctx, desc)
		require.NoError(t, err)
		got, _ := io.ReadAll(reader2)
		_ = reader2.Close()
		assert.Equal(t, blobData, got)
		assert.Equal(t, int32(1), inner.FetchBlobCount.Load())
	})

	t.Run("DeduplicatesConcurrentCalls", func(t *testing.T) {
		inner := newBlockingFakeRepo()
		blobData := []byte("hello blob")
		desc := inner.AddBlob(blobData, "")

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		const n = 10
		var wg sync.WaitGroup
		results := make([][]byte, n)
		errs := make([]error, n)

		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				reader, err := repo.FetchBlob(ctx, desc)
				if err != nil {
					errs[i] = err
					return
				}
				results[i], errs[i] = io.ReadAll(reader)
				_ = reader.Close()
			}()
		}

		close(inner.gate)
		wg.Wait()

		for i := range n {
			require.NoError(t, errs[i])
			assert.Equal(t, blobData, results[i])
		}
		assert.Equal(t, int32(1), inner.FetchBlobCount.Load(), "inner should be called exactly once")
	})

	t.Run("SingleflightInnerRecheckHitsCache", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		blobData := []byte("hello blob")
		desc := inner.AddBlob(blobData, "")

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		// Pre-populate the disk cache as if a previous singleflight completed
		blobPath := filepath.Join(repo.blobsDir(), desc.Digest.String())
		require.NoError(t, repo.cacheFile(blobPath, bytes.NewReader(blobData)))

		// Call fetchAndCacheBlob directly — the inner re-check should
		// find the cached file and return without calling the inner repo.
		err = repo.fetchAndCacheBlob(ctx, desc, blobPath)
		require.NoError(t, err)
		assert.Equal(t, int32(0), inner.FetchBlobCount.Load(), "inner should not be called")
	})
}

func TestCachingRepository_Close(t *testing.T) {
	t.Run("RemovesCacheDirAndClosesInner", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)

		cacheDir := repo.cacheDir
		_, err = os.Stat(cacheDir)
		require.NoError(t, err, "cache dir should exist before close")

		require.NoError(t, repo.Close())

		_, err = os.Stat(cacheDir)
		assert.True(t, os.IsNotExist(err), "cache dir should be removed after close")
		assert.Equal(t, int32(1), inner.CloseCount.Load())
	})

	t.Run("PropagatesInnerCloseError", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		inner.CloseErr = fmt.Errorf("close failed")
		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)

		err = repo.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close failed")
	})
}

func TestCachingRepository_CachedDescriptors(t *testing.T) {
	ctx := context.Background()

	t.Run("EmptyCache", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		descs := repo.CachedDescriptors()
		assert.Empty(t, descs)
	})

	t.Run("IncludesFetchedManifests", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			MediaType: ocispecv1.MediaTypeImageManifest,
		})
		desc := inner.AddManifest(manifestData, ocispecv1.MediaTypeImageManifest)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		_, _, err = repo.FetchManifest(ctx, desc)
		require.NoError(t, err)

		cached := repo.CachedDescriptors()
		require.Len(t, cached, 1)
		assert.Equal(t, desc.Digest, cached[0].Digest)
		assert.Equal(t, ocispecv1.MediaTypeImageManifest, cached[0].MediaType)
		assert.Equal(t, int64(len(manifestData)), cached[0].Size)
	})

	t.Run("IncludesFetchedBlobs", func(t *testing.T) {
		inner := testutil.NewFakeRepo()
		blobData := []byte("cached blob")
		desc := inner.AddBlob(blobData, ocispecv1.MediaTypeImageLayerGzip)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		reader, err := repo.FetchBlob(ctx, desc)
		require.NoError(t, err)
		_, _ = io.ReadAll(reader)
		_ = reader.Close()

		cached := repo.CachedDescriptors()
		require.Len(t, cached, 1)
		assert.Equal(t, desc.Digest, cached[0].Digest)
		assert.Equal(t, desc.Size, cached[0].Size)
		assert.Equal(t, ocispecv1.MediaTypeImageLayerGzip, cached[0].MediaType)
	})

	t.Run("IncludesBothManifestsAndBlobs", func(t *testing.T) {
		inner := testutil.NewFakeRepo()

		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			MediaType: ocispecv1.MediaTypeImageManifest,
		})
		manifestDesc := inner.AddManifest(manifestData, ocispecv1.MediaTypeImageManifest)

		blobData := []byte("blob content")
		blobDesc := inner.AddBlob(blobData, ocispecv1.MediaTypeImageLayerGzip)

		repo, err := NewCachingRepository(inner)
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo.Close() })

		_, _, err = repo.FetchManifest(ctx, manifestDesc)
		require.NoError(t, err)

		reader, err := repo.FetchBlob(ctx, blobDesc)
		require.NoError(t, err)
		_, _ = io.ReadAll(reader)
		_ = reader.Close()

		cached := repo.CachedDescriptors()
		assert.Len(t, cached, 2)

		digests := make(map[string]bool)
		for _, d := range cached {
			digests[d.Digest.String()] = true
		}
		assert.True(t, digests[manifestDesc.Digest.String()])
		assert.True(t, digests[blobDesc.Digest.String()])
	})
}
