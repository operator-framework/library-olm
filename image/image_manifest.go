package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// FetchImageConfig fetches and decodes the OCI image config from manifest bytes.
// It parses manifestBytes to find the config descriptor, fetches the config blob
// from repo, and decodes it into an [ocispecv1.Image].
//
// Returns an error if manifestBytes is not valid JSON, if the config blob cannot
// be fetched, or if the config blob is not a valid OCI image config.
func FetchImageConfig(ctx context.Context, repo Repository, manifestBytes []byte) (*ocispecv1.Image, error) {
	var manifest ocispecv1.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	reader, err := repo.FetchBlob(ctx, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("fetching config blob: %w", err)
	}

	cfg, err := decodeImageConfig(reader)
	return cfg, errors.Join(err, reader.Close())
}

func decodeImageConfig(reader io.Reader) (*ocispecv1.Image, error) {
	var cfg ocispecv1.Image
	if err := json.NewDecoder(reader).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	return &cfg, nil
}
