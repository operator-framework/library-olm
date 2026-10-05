package catalogmigration

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
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
	sharedA.Spec.Secrets = []string{"pull-secret"}
	different := image("shared", "three", "registry/other:1")
	configmap := &operatorsv1alpha1.CatalogSource{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "one"}, Spec: operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeConfigmap}}
	existing := &ocv1.ClusterCatalog{ObjectMeta: metav1.ObjectMeta{Name: "already"}, Spec: ocv1.ClusterCatalogSpec{Source: ocv1.CatalogSource{Image: &ocv1.ImageSource{Ref: "registry/covered:1"}}}}
	covered := image("covered", "one", "registry/covered:1")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sharedA, sharedB, different, configmap, existing, covered).Build()
	cm := NewCatalogMigrator(client)
	var events []migration.ProgressEvent
	cm.Progress = func(event migration.ProgressEvent) { events = append(events, event) }
	results, err := cm.MigrateCatalogs(context.Background(), CatalogMigratorOptions{DryRun: true})
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
	var scanned, skipped, previewed, noted bool
	for _, event := range events {
		scanned = scanned || (event.Step == migration.ProgressStepScan && event.Status == migration.ProgressCompleted)
		skipped = skipped || (event.Target == "one/cm" && event.Status == migration.ProgressWarning)
		previewed = previewed || (event.Target == "one/covered" && event.Status == migration.ProgressCompleted)
		noted = noted || (event.Target == "one/shared" && event.Status == migration.ProgressNote)
	}
	if !scanned || !skipped || !previewed || !noted {
		t.Fatalf("missing scan, source, or informational progress events: %#v", events)
	}
}

type catalogListFailure struct{ client.Client }

func (c catalogListFailure) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*operatorsv1alpha1.CatalogSourceList); ok {
		return errors.New("catalog listing denied")
	}
	return c.Client.List(ctx, list, opts...)
}

type catalogCreateFailure struct {
	client.Client
	cause error
}

func (c catalogCreateFailure) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*ocv1.ClusterCatalog); ok {
		return c.cause
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestMigrateCatalogsReportsPerSourceFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := operatorsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := ocv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	source := &operatorsv1alpha1.CatalogSource{
		ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "ns"},
		Spec:       operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeGrpc, Image: "example.com/catalog:v1"},
	}
	cause := errors.New("catalog creation denied")
	client := catalogCreateFailure{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build(), cause: cause}
	cm := NewCatalogMigrator(client)
	var events []migration.ProgressEvent
	cm.Progress = func(event migration.ProgressEvent) { events = append(events, event) }
	results, err := cm.MigrateCatalogs(t.Context(), CatalogMigratorOptions{})
	if err != nil || len(results) != 1 || results[0].Status != "error" {
		t.Fatalf("results = %#v, error = %v", results, err)
	}
	found := false
	for _, event := range events {
		if event.Target == "ns/source" && event.Status == migration.ProgressFailed && errors.Is(event.Err, cause) && strings.Contains(event.Err.Error(), "catalog creation denied") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing per-source failure event: %#v", events)
	}
}

func TestMigrateCatalogsReportsListFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := operatorsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cm := NewCatalogMigrator(catalogListFailure{Client: fake.NewClientBuilder().WithScheme(scheme).Build()})
	var events []migration.ProgressEvent
	cm.Progress = func(event migration.ProgressEvent) { events = append(events, event) }
	_, err := cm.MigrateCatalogs(t.Context(), CatalogMigratorOptions{})
	if err == nil || len(events) != 2 || events[0].Status != migration.ProgressStarted || events[1].Status != migration.ProgressFailed || !errors.Is(events[1].Err, err) {
		t.Fatalf("MigrateCatalogs() error = %v, events = %#v", err, events)
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

func TestMigrateCatalogsSkipsUnsupportedSourcesWithoutMutation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := operatorsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := ocv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	sources := []runtime.Object{
		&operatorsv1alpha1.CatalogSource{ObjectMeta: metav1.ObjectMeta{Name: "configmap", Namespace: "ns"}, Spec: operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeConfigmap}},
		&operatorsv1alpha1.CatalogSource{ObjectMeta: metav1.ObjectMeta{Name: "address-only", Namespace: "ns"}, Spec: operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeGrpc, Address: "catalog.ns.svc:50051"}},
		&operatorsv1alpha1.CatalogSource{ObjectMeta: metav1.ObjectMeta{Name: "unknown", Namespace: "ns"}, Spec: operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceType("unsupported")}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(sources...).Build()
	results, err := NewCatalogMigrator(c).MigrateCatalogs(context.Background(), CatalogMigratorOptions{})
	if err != nil || len(results) != len(sources) {
		t.Fatalf("MigrateCatalogs() = %#v, %v", results, err)
	}
	for _, result := range results {
		if result.Status != "skipped" || result.ClusterCatalogName != "" || result.Reason == "" {
			t.Fatalf("unsupported source result = %#v", result)
		}
	}
	var catalogs ocv1.ClusterCatalogList
	if err := c.List(context.Background(), &catalogs); err != nil {
		t.Fatal(err)
	}
	if len(catalogs.Items) != 0 {
		t.Fatalf("unsupported CatalogSources created ClusterCatalogs: %#v", catalogs.Items)
	}
}

func TestMigrateCatalogsReportsNameCollisionWithoutMutation(t *testing.T) {
	scheme := catalogMigrationScheme(t)
	source := imageCatalogSource("catalog", "tenant", "registry.example/new:1")
	existing := &ocv1.ClusterCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog"},
		Spec:       ocv1.ClusterCatalogSpec{Source: ocv1.CatalogSource{Image: &ocv1.ImageSource{Ref: "registry.example/old:1"}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, existing).Build()

	results, err := NewCatalogMigrator(c).MigrateCatalogs(context.Background(), CatalogMigratorOptions{})
	if err != nil || len(results) != 1 {
		t.Fatalf("MigrateCatalogs() = %#v, %v", results, err)
	}
	if results[0].Status != "error" || results[0].ClusterCatalogName != "catalog" {
		t.Fatalf("collision result = %#v", results[0])
	}

	var gotSource operatorsv1alpha1.CatalogSource
	if err := c.Get(context.Background(), client.ObjectKey{Name: source.Name, Namespace: source.Namespace}, &gotSource); err != nil {
		t.Fatalf("get CatalogSource after collision: %v", err)
	}
	var gotCatalog ocv1.ClusterCatalog
	if err := c.Get(context.Background(), client.ObjectKey{Name: existing.Name}, &gotCatalog); err != nil {
		t.Fatalf("get ClusterCatalog after collision: %v", err)
	}
	if gotCatalog.Spec.Source.Image.Ref != "registry.example/old:1" {
		t.Fatalf("existing ClusterCatalog image = %q, want unchanged", gotCatalog.Spec.Source.Image.Ref)
	}
}

func TestMigrateCatalogsDeletesEveryUnreferencedConsolidatedSource(t *testing.T) {
	scheme := catalogMigrationScheme(t)
	first := imageCatalogSource("shared", "one", "registry.example/shared:1")
	second := imageCatalogSource("shared", "two", "registry.example/shared:1")
	existing := &ocv1.ClusterCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "shared"},
		Spec:       ocv1.ClusterCatalogSpec{Source: ocv1.CatalogSource{Image: &ocv1.ImageSource{Ref: "registry.example/shared:1"}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(first, second, existing).Build()

	results, err := NewCatalogMigrator(c).MigrateCatalogs(context.Background(), CatalogMigratorOptions{DeleteCatalogSource: true})
	if err != nil || len(results) != 2 {
		t.Fatalf("MigrateCatalogs() = %#v, %v", results, err)
	}
	for _, result := range results {
		if result.Status != "adopted" || !containsNote(result.Notes, "deleted unreferenced CatalogSource") {
			t.Fatalf("consolidated source result = %#v", result)
		}
	}
	for _, source := range []*operatorsv1alpha1.CatalogSource{first, second} {
		var got operatorsv1alpha1.CatalogSource
		if err := c.Get(context.Background(), client.ObjectKey{Name: source.Name, Namespace: source.Namespace}, &got); err == nil {
			t.Fatalf("CatalogSource %s/%s was not deleted", source.Namespace, source.Name)
		}
	}
}

func TestMigrateCatalogsRetainsReferencedSource(t *testing.T) {
	scheme := catalogMigrationScheme(t)
	source := imageCatalogSource("catalog", "tenant", "registry.example/catalog:1")
	existing := &ocv1.ClusterCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog"},
		Spec:       ocv1.ClusterCatalogSpec{Source: ocv1.CatalogSource{Image: &ocv1.ImageSource{Ref: source.Spec.Image}}},
	}
	referenced := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "workload"}, Spec: &operatorsv1alpha1.SubscriptionSpec{CatalogSource: source.Name, CatalogSourceNamespace: source.Namespace}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, existing, referenced).Build()

	results, err := NewCatalogMigrator(c).MigrateCatalogs(context.Background(), CatalogMigratorOptions{DeleteCatalogSource: true})
	if err != nil || len(results) != 1 || !containsNote(results[0].Notes, "CatalogSource retained") {
		t.Fatalf("referenced result = %#v, %v", results, err)
	}
	var retained operatorsv1alpha1.CatalogSource
	if err := c.Get(context.Background(), client.ObjectKey{Name: source.Name, Namespace: source.Namespace}, &retained); err != nil {
		t.Fatalf("referenced CatalogSource was deleted: %v", err)
	}
}

func TestMigrateCatalogsDryRunReportsUnreferencedSourceDeletion(t *testing.T) {
	scheme := catalogMigrationScheme(t)
	source := imageCatalogSource("catalog", "tenant", "registry.example/catalog:1")
	existing := &ocv1.ClusterCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog"},
		Spec:       ocv1.ClusterCatalogSpec{Source: ocv1.CatalogSource{Image: &ocv1.ImageSource{Ref: source.Spec.Image}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, existing).Build()

	results, err := NewCatalogMigrator(c).MigrateCatalogs(context.Background(), CatalogMigratorOptions{DryRun: true, DeleteCatalogSource: true})
	if err != nil || len(results) != 1 || !containsNote(results[0].Notes, "would delete unreferenced CatalogSource") {
		t.Fatalf("dry-run result = %#v, %v", results, err)
	}
	var retained operatorsv1alpha1.CatalogSource
	if err := c.Get(context.Background(), client.ObjectKey{Name: source.Name, Namespace: source.Namespace}, &retained); err != nil {
		t.Fatalf("dry-run deleted CatalogSource: %v", err)
	}
}

func TestResolveExistingClusterCatalogPrefersExpectedName(t *testing.T) {
	first := &ocv1.ClusterCatalog{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	preferred := &ocv1.ClusterCatalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog"}}
	if got := resolveExistingClusterCatalog([]*ocv1.ClusterCatalog{first, preferred}, "catalog"); got != preferred {
		t.Fatalf("resolved catalog = %s, want %s", got.Name, preferred.Name)
	}
	if got := resolveExistingClusterCatalog([]*ocv1.ClusterCatalog{preferred, first}, "other"); got != first {
		t.Fatalf("fallback catalog = %s, want lexicographically first", got.Name)
	}
}

func catalogMigrationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := operatorsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := ocv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func imageCatalogSource(name, namespace, ref string) *operatorsv1alpha1.CatalogSource {
	return &operatorsv1alpha1.CatalogSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeGrpc, Image: ref},
	}
}

func containsNote(notes []string, want string) bool {
	for _, note := range notes {
		if note == want || strings.HasPrefix(note, want) {
			return true
		}
	}
	return false
}
