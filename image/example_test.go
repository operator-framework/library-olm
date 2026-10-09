package image_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"

	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"

	imgbundle "github.com/operator-framework/library-olm/image/bundle"
	imgcatalog "github.com/operator-framework/library-olm/image/catalog"
	"github.com/operator-framework/library-olm/image/internal/testutil"
)

// ExampleRegistryV1Handler demonstrates unpacking a registry+v1 operator
// bundle image by resolving the image and calling the handler directly.
func ExampleRegistryV1Handler() {
	repo := testutil.NewFakeRepo()

	layer, err := testutil.BuildTarLayer(map[string]string{
		"manifests/csv.yaml":          "apiVersion: operators.coreos.com/v1alpha1",
		"metadata/annotations.yaml":   "annotations: {}",
		"other/should-not-be-present": "nope",
	})
	if err != nil {
		log.Fatal(err)
	}
	layerDesc := repo.AddBlob(layer, ocispecv1.MediaTypeImageLayerGzip)
	desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
		imgbundle.BundleMediaTypeLabel: imgbundle.BundleMediaTypeRegistryV1,
	}, ocispecv1.MediaTypeImageManifest, layerDesc)

	dest, err := os.MkdirTemp("", "example-regv1-*")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dest) }()

	handler := &imgbundle.RegistryV1Handler{}
	if err := handler.Unpack(context.Background(), repo, desc, manifestBytes, dest); err != nil {
		log.Fatal(err)
	}

	printTree(dest)

	// Output:
	// manifests/csv.yaml
	// metadata/annotations.yaml
}

// ExampleHelmChartHandler demonstrates unpacking a Helm chart OCI artifact.
func ExampleHelmChartHandler() {
	repo := testutil.NewFakeRepo()

	configDesc := repo.AddBlob(
		testutil.MustJSON(map[string]string{"apiVersion": "v2", "name": "mychart", "version": "0.1.0"}),
		imgbundle.HelmConfigMediaType,
	)
	chartDesc := repo.AddBlob([]byte("fake-tgz-content"), imgbundle.HelmChartContentMediaType)

	manifestBytes := testutil.BuildManifest(configDesc, chartDesc)
	desc := repo.AddManifest(manifestBytes, ocispecv1.MediaTypeImageManifest)

	dest, err := os.MkdirTemp("", "example-helm-*")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dest) }()

	handler := &imgbundle.HelmChartHandler{}
	if err := handler.Unpack(context.Background(), repo, desc, manifestBytes, dest); err != nil {
		log.Fatal(err)
	}

	printTree(dest)

	// Output:
	// mychart-0.1.0.tgz
}

// ExampleFBCHandler demonstrates unpacking a file-based catalog image.
// The FBC handler extracts only the configured catalog directory and rewrites
// its contents to the root of the destination.
func ExampleFBCHandler() {
	repo := testutil.NewFakeRepo()

	layer, err := testutil.BuildTarLayer(map[string]string{
		"configs/operators/package.json": `{"schema":"olm.package","name":"my-operator"}`,
		"configs/operators/channel.json": `{"schema":"olm.channel","name":"stable"}`,
		"other/not-extracted":            "nope",
	})
	if err != nil {
		log.Fatal(err)
	}
	layerDesc := repo.AddBlob(layer, ocispecv1.MediaTypeImageLayerGzip)
	desc, manifestBytes := testutil.SetupSingleManifest(repo, map[string]string{
		imgcatalog.ConfigDirLabel: "/configs",
	}, ocispecv1.MediaTypeImageManifest, layerDesc)

	dest, err := os.MkdirTemp("", "example-fbc-*")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dest) }()

	handler := &imgcatalog.FBCHandler{}
	if err := handler.Unpack(context.Background(), repo, desc, manifestBytes, dest); err != nil {
		log.Fatal(err)
	}

	printTree(dest)

	// Output:
	// operators/channel.json
	// operators/package.json
}

// --- helpers ---

func printTree(root string) {
	var files []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, _ error) error {
		if !info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
		}
		return nil
	})
	slices.Sort(files)
	for _, f := range files {
		fmt.Println(f)
	}
}
