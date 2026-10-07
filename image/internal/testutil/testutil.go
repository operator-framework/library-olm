// Package testutil provides shared test infrastructure for image handler tests.
package testutil

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FakeManifest holds raw manifest bytes and their media type.
type FakeManifest struct {
	Bytes     []byte
	MediaType string
}

// FakeRepo implements [image.Repository] for testing. It returns canned
// manifests and blobs keyed by digest, and tracks call counts for
// cache verification tests.
type FakeRepo struct {
	Manifests map[string]FakeManifest
	Blobs     map[string]func() io.ReadCloser

	// ResolveDesc and ResolveErr control the return values of Resolve.
	ResolveDesc ocispecv1.Descriptor
	ResolveErr  error

	// CloseErr controls the return value of Close.
	CloseErr error

	// Counters track how many times each method was called.
	ResolveCount       atomic.Int32
	FetchManifestCount atomic.Int32
	FetchBlobCount     atomic.Int32
	CloseCount         atomic.Int32
}

// NewFakeRepo creates a FakeRepo with empty manifest and blob stores.
func NewFakeRepo() *FakeRepo {
	return &FakeRepo{
		Manifests: make(map[string]FakeManifest),
		Blobs:     make(map[string]func() io.ReadCloser),
	}
}

func (r *FakeRepo) Close() error {
	r.CloseCount.Add(1)
	return r.CloseErr
}

func (r *FakeRepo) Resolve(_ context.Context) (ocispecv1.Descriptor, error) {
	r.ResolveCount.Add(1)
	return r.ResolveDesc, r.ResolveErr
}

func (r *FakeRepo) FetchManifest(_ context.Context, desc ocispecv1.Descriptor) ([]byte, string, error) {
	r.FetchManifestCount.Add(1)
	m, ok := r.Manifests[desc.Digest.String()]
	if !ok {
		return nil, "", fmt.Errorf("manifest not found: %s", desc.Digest)
	}
	return m.Bytes, m.MediaType, nil
}

func (r *FakeRepo) FetchBlob(_ context.Context, desc ocispecv1.Descriptor) (io.ReadCloser, error) {
	r.FetchBlobCount.Add(1)
	factory, ok := r.Blobs[desc.Digest.String()]
	if !ok {
		return nil, fmt.Errorf("blob not found: %s", desc.Digest)
	}
	return factory(), nil
}

// AddManifest stores a manifest and returns its descriptor.
func (r *FakeRepo) AddManifest(data []byte, mediaType string) ocispecv1.Descriptor {
	d := digest.FromBytes(data)
	r.Manifests[d.String()] = FakeManifest{Bytes: data, MediaType: mediaType}
	return ocispecv1.Descriptor{
		MediaType: mediaType,
		Digest:    d,
		Size:      int64(len(data)),
	}
}

// AddBlob stores a blob and returns its descriptor.
func (r *FakeRepo) AddBlob(data []byte, mediaType string) ocispecv1.Descriptor {
	d := digest.FromBytes(data)
	r.Blobs[d.String()] = func() io.ReadCloser {
		return io.NopCloser(bytes.NewReader(data))
	}
	return ocispecv1.Descriptor{
		MediaType: mediaType,
		Digest:    d,
		Size:      int64(len(data)),
	}
}

// AddErrorBlob registers a blob that returns an error on read.
func (r *FakeRepo) AddErrorBlob(desc ocispecv1.Descriptor, readErr error) {
	r.Blobs[desc.Digest.String()] = func() io.ReadCloser {
		return io.NopCloser(&errorReader{err: readErr})
	}
}

type errorReader struct {
	err error
}

func (r *errorReader) Read(_ []byte) (int, error) {
	return 0, r.err
}

// MustJSON marshals v to JSON and panics on error.
func MustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// BuildImageConfig creates OCI image config JSON with the given labels.
func BuildImageConfig(labels map[string]string) []byte {
	return MustJSON(ocispecv1.Image{
		Config: ocispecv1.ImageConfig{
			Labels: labels,
		},
	})
}

// BuildManifest creates OCI manifest JSON with the given config and layers.
func BuildManifest(config ocispecv1.Descriptor, layers ...ocispecv1.Descriptor) []byte {
	return MustJSON(ocispecv1.Manifest{
		MediaType: ocispecv1.MediaTypeImageManifest,
		Config:    config,
		Layers:    layers,
	})
}

// SetupSingleManifest creates a single-platform manifest in the fake repo with the
// given labels on its image config and optional layers. Returns the manifest
// descriptor and raw bytes.
func SetupSingleManifest(repo *FakeRepo, labels map[string]string, mediaType string, layers ...ocispecv1.Descriptor) (ocispecv1.Descriptor, []byte) {
	configBlob := BuildImageConfig(labels)
	configDesc := repo.AddBlob(configBlob, ocispecv1.MediaTypeImageConfig)
	manifestBytes := BuildManifest(configDesc, layers...)
	desc := repo.AddManifest(manifestBytes, mediaType)
	return desc, manifestBytes
}

// BuildTarLayer creates a tar archive containing files with the given name→content mapping.
// Files are written in iteration order; callers needing deterministic output
// should sort the keys beforehand.
func BuildTarLayer(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}); err != nil {
			return nil, fmt.Errorf("writing header %q: %w", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return nil, fmt.Errorf("writing content %q: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar: %w", err)
	}
	return buf.Bytes(), nil
}

// AssertFileExists asserts that the file at path exists.
func AssertFileExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	assert.NoError(t, err, "expected file to exist: %s", path)
}

// AssertFileNotExists asserts that the file at path does not exist.
func AssertFileNotExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist, "expected file to not exist: %s", path)
}

// AssertFileContent asserts that the file at path has the expected content.
func AssertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err, "reading file: %s", path)
	assert.Equal(t, expected, string(content))
}
