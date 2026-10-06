package image

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/google/renameio/v2"
	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/manifest"
	"golang.org/x/sync/singleflight"
)

// syncMap is a type-safe wrapper around sync.Map.
type syncMap[K comparable, V any] struct {
	m sync.Map
}

func (m *syncMap[K, V]) Load(key K) (V, bool) {
	v, ok := m.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return v.(V), true
}

func (m *syncMap[K, V]) Store(key K, value V) {
	m.m.Store(key, value)
}

func (m *syncMap[K, V]) Range(f func(K, V) bool) {
	m.m.Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}

// Repository provides read access to an OCI image repository. Implementations
// handle the transport-specific details of resolving references, fetching
// manifests, and fetching blobs.
//
// Callers must call [Repository.Close] when done to release resources.
type Repository interface {
	// Named returns the repository reference. The returned value will
	// implement [reference.NamedTagged] or [reference.Canonical].
	Named() reference.Named

	// Resolve resolves the repository reference to a content descriptor.
	// The returned descriptor contains the digest, media type, and size
	// of the manifest. Returns an error if the reference cannot be resolved
	// (e.g. network failure, image not found).
	Resolve(ctx context.Context) (ocispecv1.Descriptor, error)

	// FetchManifest fetches a manifest by digest and returns the raw bytes
	// and media type. Returns an error if the digest is not found or cannot
	// be fetched.
	FetchManifest(ctx context.Context, desc ocispecv1.Descriptor) ([]byte, string, error)

	// FetchBlob fetches a blob by digest. The caller must close the returned
	// reader. Returns an error if the blob is not found or cannot be fetched.
	FetchBlob(ctx context.Context, desc ocispecv1.Descriptor) (io.ReadCloser, error)

	// Close releases any resources held by the repository (network
	// connections, temporary files, etc.).
	Close() error
}

// CachingRepository wraps a [Repository] with in-memory manifest caching and
// on-disk blob caching. Concurrent fetches for the same digest are deduplicated
// via singleflight. The cache is stored in a temporary directory that is removed
// when [CachingRepository.Close] is called.
type CachingRepository struct {
	inner Repository

	// Local storage
	cacheDir string

	// Concurrency control
	group      singleflight.Group
	resolution atomic.Pointer[ocispecv1.Descriptor]
	manifests  syncMap[string, *cachedManifest]      // digest → manifest
	blobDescs  syncMap[string, ocispecv1.Descriptor] // digest → descriptor
}

// NewCachingRepository wraps client with a local cache. It creates a temporary
// directory for blob storage; the caller must call [CachingRepository.Close] to
// clean it up. Returns an error if the temporary directory cannot be created.
func NewCachingRepository(client Repository) (*CachingRepository, error) {
	cacheDir, err := os.MkdirTemp("", "oci-session-")
	if err != nil {
		return nil, err
	}

	return &CachingRepository{
		inner:    client,
		cacheDir: cacheDir,
	}, nil
}

func (s *CachingRepository) Named() reference.Named {
	return s.inner.Named()
}

func (s *CachingRepository) Close() error {
	return errors.Join(s.inner.Close(), os.RemoveAll(s.cacheDir))
}

func (s *CachingRepository) blobsDir() string {
	return filepath.Join(s.cacheDir, "blobs")
}

// CachedDescriptors returns descriptors for all content currently in the cache,
// including both manifests and blobs. The returned slice is a snapshot; subsequent
// fetches will not be reflected.
func (s *CachingRepository) CachedDescriptors() []ocispecv1.Descriptor {
	var descs []ocispecv1.Descriptor
	s.manifests.Range(func(digestStr string, m *cachedManifest) bool {
		descs = append(descs, ocispecv1.Descriptor{
			MediaType: m.mediaType,
			Digest:    digest.Digest(digestStr),
			Size:      int64(len(m.bytes)),
		})
		return true
	})
	s.blobDescs.Range(func(_ string, desc ocispecv1.Descriptor) bool {
		descs = append(descs, desc)
		return true
	})
	return descs
}

func (s *CachingRepository) Resolve(ctx context.Context) (ocispecv1.Descriptor, error) {
	// Fast path: return cached result without entering singleflight.
	if desc := s.resolution.Load(); desc != nil {
		return *desc, nil
	}

	v, err, _ := s.group.Do("resolve", func() (any, error) {
		return s.fetchAndCacheResolution(ctx)
	})
	if err != nil {
		return ocispecv1.Descriptor{}, err
	}
	return v.(ocispecv1.Descriptor), nil
}

// fetchAndCacheResolution is the singleflight body for Resolve. It re-checks
// the cache (a previous singleflight group may have populated it) and fetches
// from the inner repo on miss.
func (s *CachingRepository) fetchAndCacheResolution(ctx context.Context) (ocispecv1.Descriptor, error) {
	if desc := s.resolution.Load(); desc != nil {
		return *desc, nil
	}
	desc, err := s.inner.Resolve(ctx)
	if err != nil {
		return ocispecv1.Descriptor{}, err
	}
	s.resolution.Store(&desc)
	return desc, nil
}

type cachedManifest struct {
	bytes     []byte
	mediaType string
}

func (s *CachingRepository) FetchManifest(ctx context.Context, desc ocispecv1.Descriptor) ([]byte, string, error) {
	digestKey := desc.Digest.String()

	if m, ok := s.manifests.Load(digestKey); ok {
		return m.bytes, m.mediaType, nil
	}

	v, err, _ := s.group.Do("manifest:"+digestKey, func() (any, error) {
		return s.fetchAndCacheManifest(ctx, desc, digestKey)
	})
	if err != nil {
		return nil, "", err
	}
	result := v.(*cachedManifest)
	return result.bytes, result.mediaType, nil
}

// fetchAndCacheManifest is the singleflight body for FetchManifest. It re-checks
// the cache (a previous singleflight group may have populated it) and fetches
// from the inner repo on miss.
func (s *CachingRepository) fetchAndCacheManifest(ctx context.Context, desc ocispecv1.Descriptor, digestKey string) (*cachedManifest, error) {
	if m, ok := s.manifests.Load(digestKey); ok {
		return m, nil
	}

	raw, mediaType, err := s.inner.FetchManifest(ctx, desc)
	if err != nil {
		return nil, err
	}

	m := &cachedManifest{bytes: raw, mediaType: mediaType}
	s.manifests.Store(digestKey, m)
	return m, nil
}

func (s *CachingRepository) FetchBlob(ctx context.Context, desc ocispecv1.Descriptor) (io.ReadCloser, error) {
	blobPath := filepath.Join(s.blobsDir(), desc.Digest.String())

	if f, err := os.Open(blobPath); err == nil {
		s.blobDescs.Store(desc.Digest.String(), desc)
		return f, nil
	}

	_, err, _ := s.group.Do("blob:"+desc.Digest.String(), func() (any, error) {
		return nil, s.fetchAndCacheBlob(ctx, desc, blobPath)
	})
	if err != nil {
		return nil, err
	}
	return os.Open(blobPath)
}

// fetchAndCacheBlob is the singleflight body for FetchBlob. It re-checks
// the disk cache (a previous singleflight group may have written it) and
// fetches from the inner repo on miss.
func (s *CachingRepository) fetchAndCacheBlob(ctx context.Context, desc ocispecv1.Descriptor, blobPath string) error {
	if _, err := os.Stat(blobPath); err == nil {
		s.blobDescs.Store(desc.Digest.String(), desc)
		return nil
	}

	reader, err := s.inner.FetchBlob(ctx, desc)
	if err != nil {
		return err
	}

	cacheErr := s.cacheFile(blobPath, reader)
	if cacheErr == nil {
		s.blobDescs.Store(desc.Digest.String(), desc)
	}
	return errors.Join(cacheErr, reader.Close())
}

func (s *CachingRepository) cacheFile(path string, reader io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := renameio.TempFile("", path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, reader); err != nil {
		return errors.Join(err, f.Cleanup())
	}
	if err := f.CloseAtomicallyReplace(); err != nil {
		return err
	}
	return nil
}

// IsIndex returns true if the media type represents an OCI index or Docker manifest list.
func IsIndex(mediaType string) bool {
	return mediaType == ocispecv1.MediaTypeImageIndex || mediaType == manifest.DockerV2ListMediaType
}

// IsManifest returns true if the media type represents an OCI manifest or Docker schema2 manifest.
func IsManifest(mediaType string) bool {
	return mediaType == ocispecv1.MediaTypeImageManifest || mediaType == manifest.DockerV2Schema2MediaType
}

// Handler knows how to identify and unpack a specific type of OCI content.
type Handler interface {
	// Name returns a human-readable identifier used in error messages and logs.
	Name() string

	// Matches inspects the resolved descriptor and manifest to determine
	// whether this handler can unpack the content.
	Matches(ctx context.Context, repo Repository, desc ocispecv1.Descriptor, manifestBytes []byte) (bool, error)

	// Discover walks the image tree and returns the complete set of descriptors
	// that [Handler.Unpack] would need to process. It fetches the minimum
	// necessary to discover the full graph (manifests and configs, not layer
	// blobs) and does not write anything to disk. When used with a
	// [CachingRepository], everything fetched during Discover is cached and
	// reused by Unpack for free.
	Discover(ctx context.Context, repo Repository, desc ocispecv1.Descriptor, manifestBytes []byte) ([]ocispecv1.Descriptor, error)

	// Unpack extracts the image content into dest. Returns an error if
	// unpacking fails (e.g. blob fetch failure, corrupt layer, I/O error).
	Unpack(ctx context.Context, repo Repository, desc ocispecv1.Descriptor, manifestBytes []byte, dest string) error
}
