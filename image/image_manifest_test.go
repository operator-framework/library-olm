package image

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operator-framework/library-olm/image/internal/testutil"
)

func TestFetchImageConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("Success", func(t *testing.T) {
		repo := testutil.NewFakeRepo()

		imgCfg := ocispecv1.Image{
			Author: "test-author",
		}
		cfgBytes := testutil.MustJSON(imgCfg)
		cfgDesc := repo.AddBlob(cfgBytes, "")

		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Config: cfgDesc,
		})

		got, err := FetchImageConfig(ctx, repo, manifestBytes)
		require.NoError(t, err)
		assert.Equal(t, "test-author", got.Author)
	})

	t.Run("InvalidManifestJSON", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		_, err := FetchImageConfig(ctx, repo, []byte("not json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing manifest")
	})

	t.Run("BlobFetchError", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Config: ocispecv1.Descriptor{Digest: digest.FromString("missing")},
		})

		_, err := FetchImageConfig(ctx, repo, manifestBytes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching config blob")
	})

	t.Run("InvalidConfigJSON", func(t *testing.T) {
		repo := testutil.NewFakeRepo()
		cfgDesc := repo.AddBlob([]byte("not json"), "")
		manifestBytes := testutil.MustJSON(ocispecv1.Manifest{
			Config: cfgDesc,
		})

		_, err := FetchImageConfig(ctx, repo, manifestBytes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decoding config")
	})
}

func TestDecodeImageConfig(t *testing.T) {
	t.Run("ValidConfig", func(t *testing.T) {
		cfg := ocispecv1.Image{Author: "me"}
		data, _ := json.Marshal(cfg)
		got, err := decodeImageConfig(bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, "me", got.Author)
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		_, err := decodeImageConfig(bytes.NewReader([]byte("{")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decoding config")
	})

	t.Run("EmptyReader", func(t *testing.T) {
		_, err := decodeImageConfig(io.LimitReader(bytes.NewReader(nil), 0))
		require.Error(t, err)
	})
}
