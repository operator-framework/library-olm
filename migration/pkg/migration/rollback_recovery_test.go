package migration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// orphanRecordingClient verifies deletion policy without relying on fake-client
// garbage collection, which does not simulate Kubernetes orphan propagation.
type orphanRecordingClient struct {
	client.Client
	policies map[string]metav1.DeletionPropagation
}

func (c orphanRecordingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	options := &client.DeleteOptions{}
	for _, option := range opts {
		option.ApplyToDelete(options)
	}
	if options.PropagationPolicy != nil {
		c.policies[obj.GetName()] = *options.PropagationPolicy
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func TestRollbackRestoresSubscriptionAndOrphansAllOwnedRevisions(t *testing.T) {
	ctx := context.Background()
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{
		MigratedFromSubscriptionAnnotation:    "source/sub",
		MigrationSubscriptionBackupAnnotation: `{"name":"widgets","source":"catalog","sourceNamespace":"olm","channel":"stable","installPlanApproval":"Manual","startingCSV":"widgets.v1"}`,
	}}}
	first := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "widgets-1", Labels: map[string]string{LabelOwnerName: ce.Name}}}
	second := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "widgets-2", Labels: map[string]string{LabelOwnerName: ce.Name}}}
	unrelated := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "other-1", Labels: map[string]string{LabelOwnerName: "other"}}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "source"}}
	og := &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "source"}}
	m := migrationTestClient(t, ce, first, second, unrelated, deployment, og, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "source"}})
	policies := map[string]metav1.DeletionPropagation{}
	m.Client = orphanRecordingClient{Client: m.Client, policies: policies}
	if err := m.Rollback(ctx, Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true}); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
	for _, object := range []client.Object{ce, first, second} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Errorf("rollback retained %T %s: %v", object, object.GetName(), err)
		}
		if policies[object.GetName()] != metav1.DeletePropagationOrphan {
			t.Errorf("deletion policy for %s = %q, want Orphan", object.GetName(), policies[object.GetName()])
		}
	}
	for _, object := range []client.Object{unrelated, deployment, og} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Errorf("rollback removed retained %T %s: %v", object, object.GetName(), err)
		}
	}
	var restored operatorsv1alpha1.Subscription
	if err := m.Client.Get(ctx, client.ObjectKey{Namespace: "source", Name: "sub"}, &restored); err != nil {
		t.Fatal(err)
	}
	want := &operatorsv1alpha1.SubscriptionSpec{Package: "widgets", CatalogSource: "catalog", CatalogSourceNamespace: "olm", Channel: "stable", InstallPlanApproval: operatorsv1alpha1.ApprovalManual, StartingCSV: "widgets.v1"}
	if !reflect.DeepEqual(restored.Spec, want) {
		t.Fatalf("restored spec = %#v, want %#v", restored.Spec, want)
	}
}

func TestRollbackLegacyRevisionAndForeignNameCollision(t *testing.T) {
	for _, owner := range []string{"", "other"} {
		t.Run("owner="+owner, func(t *testing.T) {
			ctx := context.Background()
			ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{
				MigratedFromSubscriptionAnnotation:    "source/sub",
				MigrationSubscriptionBackupAnnotation: `{"name":"widgets","source":"catalog","sourceNamespace":"olm"}`,
			}}}
			revision := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "widgets-1", Labels: map[string]string{LabelOwnerName: owner}}}
			m := migrationTestClient(t, ce, revision, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "source"}})
			err := m.Rollback(ctx, Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true})
			if owner == "" {
				if err != nil {
					t.Fatal(err)
				}
				if err := m.Client.Get(ctx, client.ObjectKeyFromObject(revision), &ocv1.ClusterObjectSet{}); !apierrors.IsNotFound(err) {
					t.Fatalf("legacy revision was not removed: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "belongs to another extension") {
				t.Fatalf("foreign revision collision error = %v", err)
			}
			for _, object := range []client.Object{ce, revision} {
				if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatalf("rollback preflight deleted %T: %v", object, err)
				}
			}
		})
	}
}

type revisionListFailureClient struct {
	client.Client
}

func (c revisionListFailureClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*ocv1.ClusterObjectSetList); ok {
		return errors.New("revision list forbidden")
	}
	return c.Client.List(ctx, list, opts...)
}

func TestRollbackRevisionListFailurePreservesBackup(t *testing.T) {
	ctx := context.Background()
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{
		MigratedFromSubscriptionAnnotation:    "source/sub",
		MigrationSubscriptionBackupAnnotation: `{"name":"widgets","source":"catalog","sourceNamespace":"olm"}`,
	}}}
	m := migrationTestClient(t, ce, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "source"}})
	m.Client = revisionListFailureClient{Client: m.Client}
	if err := m.Rollback(ctx, Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true}); err == nil || !strings.Contains(err.Error(), "revision list forbidden") {
		t.Fatalf("Rollback() error = %v, want revision list failure", err)
	}
	var retained ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(ce), &retained); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retained.Annotations, ce.Annotations) {
		t.Fatal("rollback preflight modified the backup annotations")
	}
}

type revisionDeleteFailureClient struct {
	client.Client
	failName string
	failOnce bool
}

func (c *revisionDeleteFailureClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*ocv1.ClusterObjectSet); ok && obj.GetName() == c.failName && c.failOnce {
		c.failOnce = false
		return errors.New("revision deletion forbidden")
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func TestRollbackRevisionDeleteFailureKeepsBackupForRetry(t *testing.T) {
	ctx := context.Background()
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{
		MigratedFromSubscriptionAnnotation:    "source/sub",
		MigrationSubscriptionBackupAnnotation: `{"name":"widgets","source":"catalog","sourceNamespace":"olm"}`,
	}}}
	first := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "widgets-1", Labels: map[string]string{LabelOwnerName: ce.Name}}}
	second := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "widgets-2", Labels: map[string]string{LabelOwnerName: ce.Name}}}
	m := migrationTestClient(t, ce, first, second, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "source"}})
	m.Client = &revisionDeleteFailureClient{Client: m.Client, failName: second.Name, failOnce: true}
	if err := m.Rollback(ctx, Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true}); err == nil || !strings.Contains(err.Error(), "revision deletion forbidden") {
		t.Fatalf("Rollback() error = %v, want revision deletion failure", err)
	}
	var retained ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(ce), &retained); err != nil {
		t.Fatalf("failed rollback removed the ClusterExtension backup: %v", err)
	}
	if retained.Annotations[MigrationSubscriptionBackupAnnotation] != ce.Annotations[MigrationSubscriptionBackupAnnotation] {
		t.Fatal("failed rollback changed the Subscription backup")
	}
	if err := m.Rollback(ctx, Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true}); err != nil {
		t.Fatalf("retry Rollback(): %v", err)
	}
	var restored operatorsv1alpha1.Subscription
	if err := m.Client.Get(ctx, client.ObjectKey{Namespace: "source", Name: "sub"}, &restored); err != nil {
		t.Fatalf("retry did not restore Subscription: %v", err)
	}
}

type reconcilingSubscriptionClient struct {
	client.Client
	createError error
}

func (c reconcilingSubscriptionClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if sub, ok := obj.(*operatorsv1alpha1.Subscription); ok {
		if c.createError != nil {
			return c.createError
		}
		// Simulate OLMv0 observing the recreated Subscription.
		sub.Status.State = operatorsv1alpha1.SubscriptionStateAtLatest
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestRecoverFromBackupRestoresPinnedSubscriptionWithoutChangingBackup(t *testing.T) {
	sub, _ := healthySubscriptionFixtures()
	sub.UID = "old-uid"
	sub.ResourceVersion = "123"
	sub.Spec.CatalogSource = "catalog"
	sub.Spec.CatalogSourceNamespace = "olm"
	original := sub.DeepCopy()
	m := migrationTestClient(t)
	m.Client = reconcilingSubscriptionClient{Client: m.Client}
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	ctx, cancel := NewRecoveryContext(parent)
	defer cancel()
	if err := m.RecoverFromBackup(ctx, Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}, &Backup{Subscription: sub}); err != nil {
		t.Fatalf("RecoverFromBackup(): %v", err)
	}
	var restored operatorsv1alpha1.Subscription
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(sub), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Spec.StartingCSV != original.Status.InstalledCSV || restored.UID == original.UID || restored.Status.State != operatorsv1alpha1.SubscriptionStateAtLatest {
		t.Fatalf("invalid restored Subscription: %#v", restored)
	}
	if !reflect.DeepEqual(sub, original) {
		t.Fatal("recovery modified the authoritative in-memory backup")
	}
}

func TestRecoverFromBackupReportsCreationFailure(t *testing.T) {
	sub, _ := healthySubscriptionFixtures()
	m := migrationTestClient(t)
	m.Client = reconcilingSubscriptionClient{Client: m.Client, createError: errors.New("restoration forbidden")}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.RecoverFromBackup(ctx, Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}, &Backup{Subscription: sub}); err == nil || !strings.Contains(err.Error(), "restoration forbidden") {
		t.Fatalf("RecoverFromBackup() error = %v, want creation failure", err)
	}
}

func TestRecoverFromBackupTimeoutPreservesRecreatedSubscription(t *testing.T) {
	sub, _ := healthySubscriptionFixtures()
	original := sub.DeepCopy()
	m := migrationTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := m.RecoverFromBackup(ctx, Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}, &Backup{Subscription: sub})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RecoverFromBackup() error = %v, want reconciliation timeout", err)
	}
	var restored operatorsv1alpha1.Subscription
	if err := m.Client.Get(context.Background(), client.ObjectKeyFromObject(sub), &restored); err != nil {
		t.Fatalf("reconciliation timeout deleted the restored Subscription: %v", err)
	}
	if restored.Spec.StartingCSV != original.Status.InstalledCSV || !reflect.DeepEqual(sub, original) {
		t.Fatal("reconciliation timeout changed the backup or lost the installed-version pin")
	}
}

func TestCleanupConflictPreservesSharedOperatorGroupAndOLMv1Management(t *testing.T) {
	ctx := context.Background()
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{
		MigratedFromSubscriptionAnnotation: "source/sub",
	}}, Spec: ocv1.ClusterExtensionSpec{Source: ocv1.SourceConfig{Catalog: &ocv1.CatalogFilter{PackageName: "widgets"}}}}
	cos := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "widgets-2", Labels: map[string]string{LabelOwnerName: ce.Name}}}
	sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "source"}, Status: operatorsv1alpha1.SubscriptionStatus{InstalledCSV: "widgets.v1"}}
	csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "source"}}
	condition := &operatorsv1.OperatorCondition{ObjectMeta: metav1.ObjectMeta{Name: csv.Name, Namespace: csv.Namespace}}
	copied := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: csv.Name, Namespace: "other", Labels: map[string]string{"olm.managed": "true", operatorsv1alpha1.CopiedLabelKey: csv.Namespace}}}
	unrelatedCopy := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "unrelated.v1", Namespace: "other", Labels: map[string]string{"olm.managed": "true", operatorsv1alpha1.CopiedLabelKey: csv.Namespace}}}
	foreignCopy := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: csv.Name, Namespace: "foreign", Labels: map[string]string{"olm.managed": "true", operatorsv1alpha1.CopiedLabelKey: "different-source"}}}
	otherSub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "source"}}
	group := &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "source"}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "source"}}
	m := migrationTestClient(t, ce, cos, sub, otherSub, group, deployment, csv, condition, copied, unrelatedCopy, foreignCopy)
	policies := make(map[string]metav1.DeletionPropagation)
	m.Client = orphanRecordingClient{Client: m.Client, policies: policies}
	if err := m.CleanupConflict(ctx, ce.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(sub), &operatorsv1alpha1.Subscription{}); !apierrors.IsNotFound(err) {
		t.Fatalf("cleanup retained conflicting Subscription: %v", err)
	}
	for _, object := range []client.Object{csv, condition, copied} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Fatalf("cleanup retained conflict artifact %T: %v", object, err)
		}
	}
	if policies[csv.Name] != metav1.DeletePropagationOrphan {
		t.Fatal("conflict cleanup must orphan CSV workloads")
	}
	for _, object := range []client.Object{ce, cos, otherSub, group, deployment, unrelatedCopy, foreignCopy} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatalf("cleanup deleted retained %T %s: %v", object, object.GetName(), err)
		}
	}
}

func TestCleanupConflictFindsCSVWithoutSubscriptionStatus(t *testing.T) {
	for _, properties := range []string{
		`[{"type":"olm.package","value":{"packageName":"widgets"}}]`,
		`{"properties":[{"type":"olm.package","value":{"packageName":"widgets"}}]}`,
	} {
		t.Run(properties, func(t *testing.T) {
			ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "source/sub"}}, Spec: ocv1.ClusterExtensionSpec{Source: ocv1.SourceConfig{Catalog: &ocv1.CatalogFilter{PackageName: "widgets"}}}}
			sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "source"}}
			csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "source", Annotations: map[string]string{"operatorframework.io/properties": properties}}}
			unrelated := csv.DeepCopy()
			unrelated.Name = "other.v1"
			unrelated.Annotations["operatorframework.io/properties"] = `[{"type":"olm.package","value":{"packageName":"other"}}]`
			otherNamespace := csv.DeepCopy()
			otherNamespace.Namespace = "elsewhere"
			m := migrationTestClient(t, ce, sub, csv, unrelated, otherNamespace)
			if err := m.CleanupConflict(t.Context(), ce.Name); err != nil {
				t.Fatal(err)
			}
			if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(csv), &operatorsv1alpha1.ClusterServiceVersion{}); !apierrors.IsNotFound(err) {
				t.Fatalf("cleanup retained unreferenced package CSV: %v", err)
			}
			for _, object := range []client.Object{ce, unrelated, otherNamespace} {
				if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCleanupConflictRejectsUnsafeCSVDiscovery(t *testing.T) {
	for _, scenario := range []string{"shared CSV", "shared package", "wrong Subscription package", "wrong CSV package"} {
		t.Run(scenario, func(t *testing.T) {
			ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "source/sub"}}, Spec: ocv1.ClusterExtensionSpec{Source: ocv1.SourceConfig{Catalog: &ocv1.CatalogFilter{PackageName: "widgets"}}}}
			sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "source"}, Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "widgets"}, Status: operatorsv1alpha1.SubscriptionStatus{InstalledCSV: "widgets.v1"}}
			csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "source"}}
			other := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "source"}}
			switch scenario {
			case "shared CSV":
				other.Status.CurrentCSV = csv.Name
			case "shared package":
				other.Spec = &operatorsv1alpha1.SubscriptionSpec{Package: "widgets"}
			case "wrong Subscription package":
				sub.Spec.Package = "different"
			case "wrong CSV package":
				csv.Annotations = map[string]string{"operatorframework.io/properties": `[{"type":"olm.package","value":{"packageName":"different"}}]`}
			}
			m := migrationTestClient(t, ce, sub, csv, other)
			if err := m.CleanupConflict(t.Context(), ce.Name); err == nil {
				t.Fatal("unsafe conflict cleanup unexpectedly succeeded")
			}
			for _, object := range []client.Object{ce, sub, csv, other} {
				if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatalf("cleanup preflight mutated resources: %v", err)
				}
			}
		})
	}
}

type conflictCleanupFailureClient struct {
	client.Client
	listCSV, deleteCSV, deleteCondition bool
}

func (c conflictCleanupFailureClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*operatorsv1alpha1.ClusterServiceVersionList); ok && c.listCSV {
		return errors.New("CSV listing forbidden")
	}
	return c.Client.List(ctx, list, opts...)
}

func (c conflictCleanupFailureClient) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	switch object.(type) {
	case *operatorsv1alpha1.ClusterServiceVersion:
		if c.deleteCSV {
			return errors.New("CSV deletion forbidden")
		}
	case *operatorsv1.OperatorCondition:
		if c.deleteCondition {
			return errors.New("condition deletion forbidden")
		}
	}
	return c.Client.Delete(ctx, object, opts...)
}

func TestCleanupConflictReportsCSVAndArtifactFailures(t *testing.T) {
	for _, scenario := range []string{"list", "delete CSV", "delete condition"} {
		t.Run(scenario, func(t *testing.T) {
			ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "source/sub"}}, Spec: ocv1.ClusterExtensionSpec{Source: ocv1.SourceConfig{Catalog: &ocv1.CatalogFilter{PackageName: "widgets"}}}}
			sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "source"}, Status: operatorsv1alpha1.SubscriptionStatus{InstalledCSV: "widgets.v1"}}
			csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "source"}}
			m := migrationTestClient(t, ce, sub, csv)
			m.Client = conflictCleanupFailureClient{Client: m.Client, listCSV: scenario == "list", deleteCSV: scenario == "delete CSV", deleteCondition: scenario == "delete condition"}
			if err := m.CleanupConflict(t.Context(), ce.Name); err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Fatalf("cleanup error = %v", err)
			}
			if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(ce), &ocv1.ClusterExtension{}); err != nil {
				t.Fatal("failed cleanup removed OLMv1 management")
			}
			if scenario == "list" {
				if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(sub), &operatorsv1alpha1.Subscription{}); err != nil {
					t.Fatal("discovery failure deleted Subscription")
				}
			}
		})
	}
}
