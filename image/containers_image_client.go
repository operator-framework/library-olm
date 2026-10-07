package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	ctrimage "go.podman.io/image/v5/image"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/pkg/blobinfocache/none"
	"go.podman.io/image/v5/signature"
	"go.podman.io/image/v5/transports"
	"go.podman.io/image/v5/types"
	"oras.land/oras-go/v2/content"
)

// VerificationMode produces a [signature.PolicyContext] from a
// [types.SystemContext]. The built-in modes are [VerifyAlways],
// [VerifyNever], [VerifyIfPresent], and [VerifyWithPolicy].
type VerificationMode func(*types.SystemContext) (*signature.PolicyContext, error)

var insecureAcceptAll = &signature.Policy{
	Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
}

// VerifyAlways loads the signature policy from the [types.SystemContext].
// Construction fails if no policy file is found or parsing fails.
func VerifyAlways(sysCtx *types.SystemContext) (*signature.PolicyContext, error) {
	policy, err := signature.DefaultPolicy(sysCtx)
	if err != nil {
		return nil, err
	}
	return signature.NewPolicyContext(policy)
}

// VerifyNever disables signature verification entirely. All images are
// accepted regardless of signatures.
func VerifyNever(_ *types.SystemContext) (*signature.PolicyContext, error) {
	return signature.NewPolicyContext(insecureAcceptAll)
}

// VerifyIfPresent verifies when a policy file is found, skips if no file
// exists. Fails if the file exists but cannot be parsed.
func VerifyIfPresent(sysCtx *types.SystemContext) (*signature.PolicyContext, error) {
	policy, err := signature.DefaultPolicy(sysCtx)
	if err != nil {
		if !isPolicyNotFound(err) {
			return nil, err
		}
		return signature.NewPolicyContext(insecureAcceptAll)
	}
	return signature.NewPolicyContext(policy)
}

// containers/image doesn't export a sentinel for "no policy file"; string match is the only option.
func isPolicyNotFound(err error) bool {
	return os.IsNotExist(err) || strings.Contains(err.Error(), "no policy.json file found")
}

// VerifyWithPolicy returns a [VerificationMode] that uses the provided
// [signature.Policy] directly, bypassing the [types.SystemContext] file lookup.
func VerifyWithPolicy(policy *signature.Policy) VerificationMode {
	return func(_ *types.SystemContext) (*signature.PolicyContext, error) {
		return signature.NewPolicyContext(policy)
	}
}

// Option configures a [ContainersImageRepository].
type Option func(*options)

type options struct {
	getPolicyContext VerificationMode
}

// WithSignatureVerification sets the signature verification mode.
// The default (when not called) is [VerifyAlways].
func WithSignatureVerification(mode VerificationMode) Option {
	return func(o *options) {
		o.getPolicyContext = mode
	}
}

var _ Repository = (*ContainersImageRepository)(nil)

// ContainersImageRepository implements [Repository] using the containers/image library.
// It wraps a [types.ImageSource] and verifies blob content against the requested
// descriptor using size-checked readers. When constructed with a signature policy
// (see [WithSignatureVerification]), it verifies image signatures on every
// manifest encountered.
type ContainersImageRepository struct {
	imageSource   types.ImageSource
	policyContext *signature.PolicyContext
}

// NewContainersImageRepository creates a [Repository] backed by a [types.ImageReference].
// The reference can use any transport supported by the containers/image library
// (docker, oci-layout, oci-archive, etc.). The caller must call Close on the
// returned repository to release the underlying image source.
//
// By default, signature verification is enabled ([VerifyAlways]) and requires
// a valid policy.json reachable via srcCtx. Use [WithSignatureVerification] to
// change this behavior.
//
// Returns an error if the image source cannot be created (e.g. authentication
// failure, network error, invalid reference) or if policy loading fails.
func NewContainersImageRepository(ctx context.Context, imgRef types.ImageReference, srcCtx *types.SystemContext, opts ...Option) (*ContainersImageRepository, error) {
	cfg := options{getPolicyContext: VerifyAlways}
	for _, opt := range opts {
		opt(&cfg)
	}

	policyCtx, err := cfg.getPolicyContext(srcCtx)
	if err != nil {
		return nil, err
	}

	imgSrc, err := imgRef.NewImageSource(ctx, srcCtx)
	if err != nil {
		return nil, errors.Join(err, policyCtx.Destroy())
	}

	return &ContainersImageRepository{
		imageSource:   imgSrc,
		policyContext: policyCtx,
	}, nil
}

func (c *ContainersImageRepository) Resolve(ctx context.Context) (ocispecv1.Descriptor, error) {
	manifestBytes, mediaType, err := c.getManifest(ctx, nil)
	if err != nil {
		return ocispecv1.Descriptor{}, err
	}

	imgDigest, err := manifest.Digest(manifestBytes)
	if err != nil {
		return ocispecv1.Descriptor{}, err
	}

	return ocispecv1.Descriptor{
		MediaType: mediaType,
		Digest:    imgDigest,
		Size:      int64(len(manifestBytes)),
	}, nil
}

func (c *ContainersImageRepository) Close() error {
	return errors.Join(c.policyContext.Destroy(), c.imageSource.Close())
}

func (c *ContainersImageRepository) FetchManifest(ctx context.Context, desc ocispecv1.Descriptor) ([]byte, string, error) {
	return c.getManifest(ctx, &desc.Digest)
}

func (c *ContainersImageRepository) getManifest(ctx context.Context, instanceDigest *digest.Digest) ([]byte, string, error) {
	unparsed := ctrimage.UnparsedInstance(c.imageSource, instanceDigest)
	manifestBytes, mediaType, err := unparsed.Manifest(ctx)
	if err != nil {
		return nil, "", err
	}

	if allowed, err := c.policyContext.IsRunningImageAllowed(ctx, unparsed); !allowed || err != nil {
		manifestDigest, digestErr := manifest.Digest(manifestBytes)
		if digestErr != nil {
			manifestDigest = "unknown"
		}
		if err == nil {
			err = errors.New("image rejected by policy")
		}
		return nil, "", fmt.Errorf("image signature verification failed for image %q (resolved manifest digest %s): %w", transports.ImageName(c.imageSource.Reference()), manifestDigest, err)
	}

	return manifestBytes, mediaType, nil
}

func (c *ContainersImageRepository) FetchBlob(ctx context.Context, desc ocispecv1.Descriptor) (io.ReadCloser, error) {
	blobInfo := types.BlobInfo{Digest: desc.Digest, Size: desc.Size}
	reader, _, err := c.imageSource.GetBlob(ctx, blobInfo, none.NoCache)
	if err != nil {
		return nil, err
	}

	return &blob{
		verifier: content.NewVerifyReader(reader, desc),
		closer:   reader,
	}, nil
}

type blob struct {
	verifier *content.VerifyReader
	closer   io.Closer
}

func (b *blob) Read(p []byte) (int, error) {
	n, err := b.verifier.Read(p)
	if errors.Is(err, io.EOF) {
		if verifyErr := b.verifier.Verify(); verifyErr != nil {
			return n, fmt.Errorf("verifying blob: %w", verifyErr)
		}
	}
	return n, err
}
func (b *blob) Close() error {
	return b.closer.Close()
}
