package catalogmigration

import (
	"context"
	"math"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

func TestValidatePriority(t *testing.T) {
	if got, err := validatePriority(7, false); err != nil || got != 7 {
		t.Fatalf("normal priority: %d, %v", got, err)
	}
	if _, err := validatePriority(math.MaxInt32+1, false); err == nil {
		t.Fatal("overflow accepted without acknowledgment")
	}
	if got, err := validatePriority(math.MaxInt32+1, true); err != nil || got != math.MaxInt32 {
		t.Fatalf("positive cap: %d, %v", got, err)
	}
	if got, err := validatePriority(math.MinInt32-1, true); err != nil || got != math.MinInt32 {
		t.Fatalf("negative cap: %d, %v", got, err)
	}
}

func TestConvertPollInterval(t *testing.T) {
	base := operatorsv1alpha1.CatalogSource{Spec: operatorsv1alpha1.CatalogSourceSpec{Image: "registry.example/catalog:latest"}}
	if got := convertPollInterval(base); got != 0 {
		t.Fatalf("unset interval = %d", got)
	}
	base.Spec.UpdateStrategy = &operatorsv1alpha1.UpdateStrategy{RegistryPoll: &operatorsv1alpha1.RegistryPoll{}}
	base.Spec.UpdateStrategy.Interval = &metav1.Duration{Duration: 30 * time.Second}
	if got := convertPollInterval(base); got != 1 {
		t.Fatalf("30-second interval = %d, want 1", got)
	}
	base.Spec.UpdateStrategy.Interval = &metav1.Duration{Duration: 90 * time.Second}
	if got := convertPollInterval(base); got != 1 {
		t.Fatalf("sub-minute interval = %d", got)
	}
	base.Spec.UpdateStrategy.Interval = &metav1.Duration{Duration: 5 * time.Minute}
	if got := convertPollInterval(base); got != 5 {
		t.Fatalf("five minute interval = %d", got)
	}
	base.Spec.Image = "registry.example/catalog@sha256:deadbeef"
	if got := convertPollInterval(base); got != 0 {
		t.Fatalf("digest interval = %d", got)
	}
}

func TestMigrateCatalogsDryRunAndAdoption(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := operatorsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := ocv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	image := func(name, ns, ref string) *operatorsv1alpha1.CatalogSource {
		return &operatorsv1alpha1.CatalogSource{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeGrpc, Image: ref}}
	}
	sharedA, sharedB := image("shared", "one", "registry/shared:1"), image("shared", "two", "registry/shared:1")
	different := image("shared", "three", "registry/other:1")
	configmap := &operatorsv1alpha1.CatalogSource{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "one"}, Spec: operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeConfigmap}}
	existing := &ocv1.ClusterCatalog{ObjectMeta: metav1.ObjectMeta{Name: "already"}, Spec: ocv1.ClusterCatalogSpec{Source: ocv1.CatalogSource{Image: &ocv1.ImageSource{Ref: "registry/covered:1"}}}}
	covered := image("covered", "one", "registry/covered:1")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sharedA, sharedB, different, configmap, existing, covered).Build()
	results, err := NewCatalogMigrator(client).MigrateCatalogs(context.Background(), CatalogMigratorOptions{DryRun: true})
	if err != nil || len(results) != 5 {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	byRef := map[string]CatalogMigrationResult{}
	for _, result := range results {
		byRef[result.CatalogSourceNamespace+"/"+result.CatalogSourceName] = result
	}
	// A conflicting image for the same source name makes every source in that
	// name group namespace-qualified, including the two that share an image.
	if byRef["one/shared"].ClusterCatalogName != "shared-one" || byRef["two/shared"].ClusterCatalogName != "shared-two" || byRef["three/shared"].ClusterCatalogName != "shared-three" {
		t.Fatalf("naming results: %#v", results)
	}
	if byRef["one/cm"].Status != "skipped" {
		t.Fatalf("unexpected non-image result: %#v", results)
	}
	foundSkipped, foundAdopt := false, false
	for _, result := range results {
		foundSkipped = foundSkipped || result.Status == "skipped"
		foundAdopt = foundAdopt || (result.CatalogSourceName == "covered" && result.ClusterCatalogName == "already")
	}
	if !foundSkipped || !foundAdopt {
		t.Fatalf("missing skipped/adopt results: %#v", results)
	}
}

func TestAnnotateIfNotPresent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ocv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cc := &ocv1.ClusterCatalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cc).Build()
	migrator := NewCatalogMigrator(c)
	if err := migrator.annotateIfNotPresent(context.Background(), cc, "ns/source"); err != nil {
		t.Fatal(err)
	}
	var got ocv1.ClusterCatalog
	if err := c.Get(context.Background(), client.ObjectKey{Name: "catalog"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[MigratedFromCatalogSourceAnnotation] != "ns/source" {
		t.Fatalf("annotation: %#v", got.Annotations)
	}
	if err := migrator.annotateIfNotPresent(context.Background(), &got, "other/source"); err != nil {
		t.Fatal(err)
	}
	var unchanged ocv1.ClusterCatalog
	if err := c.Get(context.Background(), client.ObjectKey{Name: "catalog"}, &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Annotations[MigratedFromCatalogSourceAnnotation] != "ns/source" {
		t.Fatalf("existing annotation was changed: %#v", unchanged.Annotations)
	}
}

func TestWaitForServing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ocv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	serving := &ocv1.ClusterCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "serving"},
		Status:     ocv1.ClusterCatalogStatus{Conditions: []metav1.Condition{{Type: "Serving", Status: metav1.ConditionTrue}}},
	}
	cm := NewCatalogMigrator(fake.NewClientBuilder().WithScheme(scheme).WithObjects(serving).Build())
	if err := cm.waitForServing(context.Background(), "serving"); err != nil {
		t.Fatalf("waitForServing(serving): %v", err)
	}
	if err := cm.waitForServing(context.Background(), "missing"); err == nil {
		t.Fatal("waitForServing(missing) unexpectedly succeeded")
	}
}
