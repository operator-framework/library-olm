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
	sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "source"}}
	otherSub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "source"}}
	group := &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "source"}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "source"}}
	m := migrationTestClient(t, ce, cos, sub, otherSub, group, deployment)
	if err := m.CleanupConflict(ctx, ce.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(sub), &operatorsv1alpha1.Subscription{}); !apierrors.IsNotFound(err) {
		t.Fatalf("cleanup retained conflicting Subscription: %v", err)
	}
	for _, object := range []client.Object{ce, cos, otherSub, group, deployment} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatalf("cleanup deleted retained %T %s: %v", object, object.GetName(), err)
		}
	}
}
