package migration

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// TestStagingBackupSurvivesFailedHandoff proves that recovery data remains on
// the server after the original caller and its in-memory backup are discarded.
func TestStagingBackupSurvivesFailedHandoff(t *testing.T) {
	for _, useCOD := range []bool{false, true} {
		name := "COS"
		if useCOD {
			name = "COD"
		}
		t.Run(name, func(t *testing.T) {
			sub, _ := healthySubscriptionFixtures()
			sub.Spec.Channel = "original-channel"
			sub.Spec.InstallPlanApproval = operatorsv1alpha1.ApprovalManual
			og := &operatorsv1.OperatorGroup{Spec: operatorsv1.OperatorGroupSpec{TargetNamespaces: []string{"watched"}}}
			var m *Migrator
			if useCOD {
				m = migrationTestClient(t, establishedClusterObjectDeploymentCRD())
			} else {
				m = migrationTestClient(t)
			}
			info := &MigrationInfo{}
			opts := Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace, ClusterExtensionName: "staged", UseClusterObjectDeployment: useCOD}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			result, err := m.CreateMigrationResources(ctx, opts, info, &Backup{Subscription: sub, OperatorGroup: og})
			if err == nil || !result.TargetMayBeActive {
				t.Fatalf("expected retained staging resources: %+v %v", result, err)
			}
			if info.SubscriptionBackupJSON != "" || info.OperatorGroupBackupJSON != "" {
				t.Fatal("modified caller's plan")
			}
			var staging client.Object = &ocv1.ClusterObjectSet{}
			stagingName := "staged-1"
			if useCOD {
				staging = &ocv1.ClusterObjectDeployment{}
				stagingName = "staged"
			}
			if err := m.Client.Get(t.Context(), client.ObjectKey{Name: stagingName}, staging); err != nil {
				t.Fatal(err)
			}
			annotations := staging.GetAnnotations()
			var spec operatorsv1alpha1.SubscriptionSpec
			if err := json.Unmarshal([]byte(annotations[MigrationSubscriptionBackupAnnotation]), &spec); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(&spec, sub.Spec) {
				t.Fatalf("lost original Subscription spec: %+v", spec)
			}
			var ogSpec operatorsv1.OperatorGroupSpec
			if err := json.Unmarshal([]byte(annotations[MigrationOperatorGroupBackupAnnotation]), &ogSpec); err != nil {
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
			// The fake has no target controller. Remove the staging object before
			// exercising explicit recovery from only the persisted Subscription spec.
			if err := m.Client.Delete(t.Context(), staging); err != nil {
				t.Fatal(err)
			}
			ns, subName, err := splitSubRef(annotations[MigratedFromSubscriptionAnnotation])
			if err != nil {
				t.Fatal(err)
			}
			fresh := &Migrator{Client: reconcilingSubscriptionClient{Client: m.Client}}
			recovered := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: subName, Namespace: ns}, Spec: &spec}
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
		})
	}
}
