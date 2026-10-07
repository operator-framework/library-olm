package image

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/directory"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/signature"
	"go.podman.io/image/v5/types"

	"github.com/operator-framework/library-olm/image/internal/testutil"
)

// fakeImageSource implements types.ImageSource for testing.
type fakeImageSource struct {
	manifests map[string]testutil.FakeManifest // nil key → primary manifest
	blobs     map[string][]byte
	ref       types.ImageReference
	closeErr  error
	closed    bool
}

func newFakeImageSource() *fakeImageSource {
	ref, _ := reference.ParseNormalizedNamed("example.com/test:latest")
	src := &fakeImageSource{
		manifests: make(map[string]testutil.FakeManifest),
		blobs:     make(map[string][]byte),
	}
	src.ref = &fakeImageReference{ref: ref, imgSrc: src}
	return src
}

func (f *fakeImageSource) setPrimaryManifest(data []byte) {
	f.manifests[""] = testutil.FakeManifest{Bytes: data, MediaType: ocispecv1.MediaTypeImageManifest}
}

func (f *fakeImageSource) setManifest(dgst digest.Digest, data []byte, mediaType string) {
	f.manifests[dgst.String()] = testutil.FakeManifest{Bytes: data, MediaType: mediaType}
}

func (f *fakeImageSource) Reference() types.ImageReference { return f.ref }

func (f *fakeImageSource) Close() error {
	f.closed = true
	return f.closeErr
}

func (f *fakeImageSource) GetManifest(_ context.Context, instanceDigest *digest.Digest) ([]byte, string, error) {
	key := ""
	if instanceDigest != nil {
		key = instanceDigest.String()
	}
	m, ok := f.manifests[key]
	if !ok {
		return nil, "", fmt.Errorf("manifest not found: %s", key)
	}
	return m.Bytes, m.MediaType, nil
}

func (f *fakeImageSource) GetBlob(_ context.Context, info types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	b, ok := f.blobs[info.Digest.String()]
	if !ok {
		return nil, -1, fmt.Errorf("blob not found: %s", info.Digest)
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func (f *fakeImageSource) HasThreadSafeGetBlob() bool { return false }

func (f *fakeImageSource) GetSignatures(_ context.Context, _ *digest.Digest) ([][]byte, error) {
	return nil, nil
}

func (f *fakeImageSource) LayerInfosForCopy(_ context.Context, _ *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}

type fakeTransport struct{}

func (fakeTransport) Name() string                                        { return "fake" }
func (fakeTransport) ParseReference(string) (types.ImageReference, error) { return nil, nil }
func (fakeTransport) ValidatePolicyConfigurationScope(string) error       { return nil }

// fakeImageReference implements types.ImageReference for testing NewContainersImageRepository.
type fakeImageReference struct {
	ref    reference.Named
	imgSrc types.ImageSource
	srcErr error
}

func (f *fakeImageReference) Transport() types.ImageTransport     { return fakeTransport{} }
func (f *fakeImageReference) StringWithinTransport() string       { return "" }
func (f *fakeImageReference) DockerReference() reference.Named    { return f.ref }
func (f *fakeImageReference) PolicyConfigurationIdentity() string { return f.ref.Name() }
func (f *fakeImageReference) PolicyConfigurationNamespaces() []string {
	return []string{reference.Domain(f.ref)}
}
func (f *fakeImageReference) DeleteImage(_ context.Context, _ *types.SystemContext) error {
	return nil
}
func (f *fakeImageReference) NewImage(_ context.Context, _ *types.SystemContext) (types.ImageCloser, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeImageReference) NewImageSource(_ context.Context, _ *types.SystemContext) (types.ImageSource, error) {
	if f.imgSrc != nil {
		if src, ok := f.imgSrc.(*fakeImageSource); ok && src.ref == nil {
			src.ref = f
		}
	}
	return f.imgSrc, f.srcErr
}
func (f *fakeImageReference) NewImageDestination(_ context.Context, _ *types.SystemContext) (types.ImageDestination, error) {
	return nil, fmt.Errorf("not implemented")
}

func mustPolicy(t *testing.T, json string) *signature.Policy {
	t.Helper()
	policy, err := signature.NewPolicyFromBytes([]byte(json))
	require.NoError(t, err)
	return policy
}

func insecureAcceptAllPolicy(t *testing.T) *signature.Policy {
	t.Helper()
	return mustPolicy(t, `{"default":[{"type":"insecureAcceptAnything"}]}`)
}

func rejectAllPolicy(t *testing.T) *signature.Policy {
	t.Helper()
	return mustPolicy(t, `{"default":[{"type":"reject"}]}`)
}

func skipVerificationPolicyContext(t *testing.T) *signature.PolicyContext {
	t.Helper()
	pc, err := VerifyNever(nil)
	require.NoError(t, err)
	return pc
}

func TestNewContainersImageRepository(t *testing.T) {
	ctx := context.Background()
	ref, _ := reference.ParseNormalizedNamed("example.com/test:latest")

	t.Run("Success", func(t *testing.T) {
		src := newFakeImageSource()
		imgRef := &fakeImageReference{ref: ref, imgSrc: src}

		repo, err := NewContainersImageRepository(ctx, imgRef, nil, WithSignatureVerification(VerifyNever))
		require.NoError(t, err)
		assert.Same(t, src, repo.imageSource)
	})

	t.Run("ImageSourceError", func(t *testing.T) {
		imgRef := &fakeImageReference{ref: ref, srcErr: fmt.Errorf("source error")}

		_, err := NewContainersImageRepository(ctx, imgRef, nil, WithSignatureVerification(VerifyNever))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source error")
	})
}

func TestContainersImageRepository_Resolve(t *testing.T) {
	ctx := context.Background()

	t.Run("Success", func(t *testing.T) {
		src := newFakeImageSource()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		src.setPrimaryManifest(manifestData)

		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		desc, err := client.Resolve(ctx)
		require.NoError(t, err)
		assert.Equal(t, ocispecv1.MediaTypeImageManifest, desc.MediaType)
		assert.Equal(t, digest.FromBytes(manifestData), desc.Digest)
		assert.Equal(t, int64(len(manifestData)), desc.Size)
	})

	t.Run("GetManifestError", func(t *testing.T) {
		src := newFakeImageSource()
		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		_, err := client.Resolve(ctx)
		require.Error(t, err)
	})

	t.Run("ErrorComputingManifestDigest", func(t *testing.T) {
		src := newFakeImageSource()
		// GuessMIMEType detects DockerV2Schema1Signed due to the "signatures" key,
		// then manifest.Digest tries to parse the JWS signature, which fails.
		src.setPrimaryManifest([]byte(`{"signatures":[{"protected":"bad"}],"schemaVersion":1}`))

		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		_, err := client.Resolve(ctx)
		require.Error(t, err)
	})
}

func TestContainersImageRepository_FetchManifest(t *testing.T) {
	ctx := context.Background()

	t.Run("Success", func(t *testing.T) {
		src := newFakeImageSource()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		dgst := digest.FromBytes(manifestData)
		src.setManifest(dgst, manifestData, ocispecv1.MediaTypeImageManifest)

		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		got, mediaType, err := client.FetchManifest(ctx, ocispecv1.Descriptor{Digest: dgst})
		require.NoError(t, err)
		assert.Equal(t, manifestData, got)
		assert.Equal(t, ocispecv1.MediaTypeImageManifest, mediaType)
	})

	t.Run("NotFound", func(t *testing.T) {
		src := newFakeImageSource()
		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		_, _, err := client.FetchManifest(ctx, ocispecv1.Descriptor{Digest: digest.FromString("missing")})
		require.Error(t, err)
	})
}

func TestContainersImageRepository_FetchBlob(t *testing.T) {
	ctx := context.Background()

	t.Run("Success", func(t *testing.T) {
		src := newFakeImageSource()
		blobData := []byte("hello blob")
		dgst := digest.FromBytes(blobData)
		src.blobs[dgst.String()] = blobData

		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		desc := ocispecv1.Descriptor{Digest: dgst, Size: int64(len(blobData))}
		reader, err := client.FetchBlob(ctx, desc)
		require.NoError(t, err)

		got, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		assert.Equal(t, blobData, got)
	})

	t.Run("NotFound", func(t *testing.T) {
		src := newFakeImageSource()
		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		_, err := client.FetchBlob(ctx, ocispecv1.Descriptor{Digest: digest.FromString("missing")})
		require.Error(t, err)
	})

	t.Run("VerifiesSize", func(t *testing.T) {
		src := newFakeImageSource()

		blobData := []byte("short")
		dgst := digest.FromBytes(blobData)
		src.blobs[dgst.String()] = blobData

		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		// Descriptor claims more bytes than actually available — VerifyReader
		// detects the premature EOF via its size check.
		desc := ocispecv1.Descriptor{Digest: dgst, Size: int64(len(blobData)) + 100}
		reader, err := client.FetchBlob(ctx, desc)
		require.NoError(t, err)

		_, err = io.ReadAll(reader)
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

func TestNewContainersImageRepository_SignatureVerification(t *testing.T) {
	ctx := context.Background()
	ref, _ := reference.ParseNormalizedNamed("example.com/test:latest")

	t.Run("DefaultFailsWithoutPolicyFile", func(t *testing.T) {
		sysCtx := &types.SystemContext{SignaturePolicyPath: "/nonexistent/policy.json"}
		src := newFakeImageSource()
		imgRef := &fakeImageReference{ref: ref, imgSrc: src}

		_, err := NewContainersImageRepository(ctx, imgRef, sysCtx)
		require.Error(t, err)
	})

	t.Run("VerifyNeverSucceeds", func(t *testing.T) {
		src := newFakeImageSource()
		imgRef := &fakeImageReference{ref: ref, imgSrc: src}

		repo, err := NewContainersImageRepository(ctx, imgRef, nil, WithSignatureVerification(VerifyNever))
		require.NoError(t, err)
		require.NoError(t, repo.Close())
	})

	t.Run("VerifyIfPresentSucceedsWithoutPolicyFile", func(t *testing.T) {
		src := newFakeImageSource()
		imgRef := &fakeImageReference{ref: ref, imgSrc: src}

		repo, err := NewContainersImageRepository(ctx, imgRef, nil, WithSignatureVerification(VerifyIfPresent))
		require.NoError(t, err)
		require.NoError(t, repo.Close())
	})

	t.Run("VerifyIfPresentFailsWithInvalidPolicyFile", func(t *testing.T) {
		policyFile := t.TempDir() + "/policy.json"
		require.NoError(t, os.WriteFile(policyFile, []byte("{not valid json"), 0o600))

		sysCtx := &types.SystemContext{SignaturePolicyPath: policyFile}
		src := newFakeImageSource()
		imgRef := &fakeImageReference{ref: ref, imgSrc: src}

		_, err := NewContainersImageRepository(ctx, imgRef, sysCtx, WithSignatureVerification(VerifyIfPresent))
		require.Error(t, err)
	})

	t.Run("VerifyWithPolicySucceeds", func(t *testing.T) {
		src := newFakeImageSource()
		imgRef := &fakeImageReference{ref: ref, imgSrc: src}

		repo, err := NewContainersImageRepository(ctx, imgRef, nil, WithSignatureVerification(VerifyWithPolicy(insecureAcceptAllPolicy(t))))
		require.NoError(t, err)
		require.NoError(t, repo.Close())
	})
}

func TestContainersImageRepository_ResolveSignatureVerification(t *testing.T) {
	ctx := context.Background()

	t.Run("AcceptAllPolicySucceeds", func(t *testing.T) {
		src := newFakeImageSource()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		src.setPrimaryManifest(manifestData)

		policyCtx, err := VerifyWithPolicy(insecureAcceptAllPolicy(t))(nil)
		require.NoError(t, err)
		client := &ContainersImageRepository{imageSource: src, policyContext: policyCtx}

		desc, err := client.Resolve(ctx)
		require.NoError(t, err)
		assert.Equal(t, digest.FromBytes(manifestData), desc.Digest)
	})

	t.Run("RejectAllPolicyFails", func(t *testing.T) {
		src := newFakeImageSource()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		src.setPrimaryManifest(manifestData)

		policyCtx, err := VerifyWithPolicy(rejectAllPolicy(t))(nil)
		require.NoError(t, err)
		client := &ContainersImageRepository{imageSource: src, policyContext: policyCtx}

		_, err = client.Resolve(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "image signature verification failed")
	})

	t.Run("RejectAllPolicyReportsNonDockerReference", func(t *testing.T) {
		src := newFakeImageSource()
		ref, err := directory.NewReference(t.TempDir())
		require.NoError(t, err)
		src.ref = ref
		src.setPrimaryManifest(testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		}))

		policyCtx, err := VerifyWithPolicy(rejectAllPolicy(t))(nil)
		require.NoError(t, err)
		client := &ContainersImageRepository{imageSource: src, policyContext: policyCtx}
		t.Cleanup(func() { assert.NoError(t, client.Close()) })

		_, err = client.Resolve(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("image signature verification failed for image %q (resolved manifest digest ", "dir:"+ref.StringWithinTransport()))
	})
}

func TestContainersImageRepository_FetchManifestSignatureVerification(t *testing.T) {
	ctx := context.Background()

	t.Run("AcceptAllPolicySucceeds", func(t *testing.T) {
		src := newFakeImageSource()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		dgst := digest.FromBytes(manifestData)
		src.setManifest(dgst, manifestData, ocispecv1.MediaTypeImageManifest)

		policyCtx, err := VerifyWithPolicy(insecureAcceptAllPolicy(t))(nil)
		require.NoError(t, err)
		client := &ContainersImageRepository{imageSource: src, policyContext: policyCtx}

		got, mediaType, err := client.FetchManifest(ctx, ocispecv1.Descriptor{Digest: dgst})
		require.NoError(t, err)
		assert.Equal(t, manifestData, got)
		assert.Equal(t, ocispecv1.MediaTypeImageManifest, mediaType)
	})

	t.Run("RejectAllPolicyFails", func(t *testing.T) {
		src := newFakeImageSource()
		manifestData := testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("cfg")},
		})
		dgst := digest.FromBytes(manifestData)
		src.setManifest(dgst, manifestData, ocispecv1.MediaTypeImageManifest)

		policyCtx, err := VerifyWithPolicy(rejectAllPolicy(t))(nil)
		require.NoError(t, err)
		client := &ContainersImageRepository{imageSource: src, policyContext: policyCtx}

		_, _, err = client.FetchManifest(ctx, ocispecv1.Descriptor{Digest: dgst})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "image signature verification failed")
	})
}

func TestContainersImageRepository_Close(t *testing.T) {
	t.Run("DelegatesToImageSource", func(t *testing.T) {
		src := newFakeImageSource()
		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		require.NoError(t, client.Close())
		assert.True(t, src.closed)
	})

	t.Run("PropagatesError", func(t *testing.T) {
		src := newFakeImageSource()
		src.closeErr = fmt.Errorf("close failed")
		client := &ContainersImageRepository{imageSource: src, policyContext: skipVerificationPolicyContext(t)}

		err := client.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close failed")
	})
}
