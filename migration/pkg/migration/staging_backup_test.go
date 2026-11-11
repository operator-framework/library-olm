package migration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// TestStagingBackupSurvivesFailedHandoff proves that recovery data remains on
// the server after the original caller and its in-memory backup are discarded.
func TestStagingBackupSurvivesFailedHandoff(t *testing.T) {
	sub, _ := healthySubscriptionFixtures()
	sub.Spec.Channel = "original-channel"
	sub.Spec.InstallPlanApproval = operatorsv1alpha1.ApprovalManual
	og := &operatorsv1.OperatorGroup{Spec: operatorsv1.OperatorGroupSpec{TargetNamespaces: []string{"watched"}}}
	m := migrationTestClient(t, establishedClusterObjectDeploymentCRD())
	info := &MigrationInfo{}
	opts := Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace, ClusterExtensionName: "staged"}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	result, err := m.CreateMigrationResources(ctx, opts, info, &Backup{Subscription: sub, OperatorGroup: og})
	if err == nil || !result.TargetMayBeActive {
		t.Fatalf("expected retained staging resources: %+v %v", result, err)
	}
	if info.SubscriptionBackupJSON != "" || info.OperatorGroupBackupJSON != "" {
		t.Fatal("modified caller's plan")
	}
	var cod ocv1.ClusterObjectDeployment
	if err := m.Client.Get(t.Context(), client.ObjectKey{Name: "staged"}, &cod); err != nil {
		t.Fatal(err)
	}
	var spec operatorsv1alpha1.SubscriptionSpec
	if err := json.Unmarshal([]byte(cod.Annotations[MigrationSubscriptionBackupAnnotation]), &spec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&spec, sub.Spec) {
		t.Fatalf("lost original Subscription spec: %+v", spec)
	}
	var ogSpec operatorsv1.OperatorGroupSpec
	if err := json.Unmarshal([]byte(cod.Annotations[MigrationOperatorGroupBackupAnnotation]), &ogSpec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ogSpec, og.Spec) {
		t.Fatal("lost OperatorGroup spec")
	}
	var extensions ocv1.ClusterExtensionList
	if err := m.Client.List(t.Context(), &extensions); err != nil {
		t.Fatal(err)
	}
	if len(extensions.Items) != 0 {
		t.Fatal("created CE after failed rollout")
	}
	// This fake has no object controller or workloads. Remove the target before
	// exercising explicit recovery using only the persisted Subscription spec.
	if err := m.Client.Delete(t.Context(), &cod); err != nil {
		t.Fatal(err)
	}
	ns, name, err := splitSubRef(cod.Annotations[MigratedFromSubscriptionAnnotation])
	if err != nil {
		t.Fatal(err)
	}
	fresh := &Migrator{Client: reconcilingSubscriptionClient{Client: m.Client}}
	recovered := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: &spec}
	if err := fresh.RecoverFromBackup(t.Context(), opts, &Backup{Subscription: recovered}); err != nil {
		t.Fatal(err)
	}
	var actual operatorsv1alpha1.Subscription
	if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(sub), &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual.Spec, sub.Spec) {
		t.Fatal("persisted backup did not restore original spec")
	}
}

// TestInitialCOSBackupSurvivesCODRejection covers the failure window unique to
// COS+COD: the initial COS is retained even when no COD or CE can be created.
func TestInitialCOSBackupSurvivesCODRejection(t *testing.T) {
	sub, _ := healthySubscriptionFixtures()
	m := migrationTestClient(t, establishedClusterObjectDeploymentCRD())
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "clusterobjectdeployments"}, "staged", errors.New("denied"))
	m.Client = interceptor.NewClient(m.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*ocv1.ClusterObjectDeployment); ok {
			return denied
		}
		if _, ok := obj.(*ocv1.ClusterExtension); ok {
			t.Fatal("created CE after rejected COD")
		}
		return c.Create(ctx, obj, opts...)
	}})
	result, err := m.CreateMigrationResources(t.Context(), Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace, ClusterExtensionName: "staged"}, &MigrationInfo{}, &Backup{Subscription: sub})
	if !errors.Is(err, denied) || !result.TargetMayBeActive {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	var cos ocv1.ClusterObjectSet
	if err := m.Client.Get(t.Context(), client.ObjectKey{Name: "staged-1"}, &cos); err != nil {
		t.Fatal(err)
	}
	var spec operatorsv1alpha1.SubscriptionSpec
	if err := json.Unmarshal([]byte(cos.Annotations[MigrationSubscriptionBackupAnnotation]), &spec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&spec, sub.Spec) {
		t.Fatal("initial COS lost Subscription backup")
	}
	var restored operatorsv1alpha1.Subscription
	if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(sub), &restored); !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected automatic recovery: %v", err)
	}
}
