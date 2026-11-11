//go:build e2e

package e2e

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

// TestCODProtectedRolloutCollision proves only the initial migration objects can
// be adopted blindly; an additional object in a later template must collide.
func TestCODProtectedRolloutCollision(t *testing.T) {
	if os.Getenv("E2E_COS_SUPERSESSION_TEST") != "true" {
		t.Skip("requires the focused handoff suite")
	}
	m, c, _ := newMigrator(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "migration-collision-"}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	name := ns.Name
	systemNamespace := ""
	t.Cleanup(func() {
		collectArtifacts(t, ns.Name)
		run(t, "kubectl", "delete", "clusterobjectdeployment/"+name, "--ignore-not-found", "--wait=true", "--timeout=60s")
		run(t, "kubectl", "delete", "clusterobjectset", "-l", migration.LabelOwnerName+"="+name, "--ignore-not-found", "--wait=true", "--timeout=60s")
		if systemNamespace != "" {
			run(t, "kubectl", "delete", "secret", "-n", systemNamespace, "-l", migration.LabelOwnerName+"="+name, "--ignore-not-found", "--wait=true", "--timeout=60s")
		}
		run(t, "kubectl", "delete", "namespace/"+ns.Name, "--wait=false")
	})
	original := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "migrated", Namespace: ns.Name}, Data: map[string]string{"value": "original"}}
	foreign := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: ns.Name}, Data: map[string]string{"value": "do-not-change"}}
	for _, obj := range []client.Object{original, foreign} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	manifest := func(name, value string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": name, "namespace": ns.Name}, "data": map[string]interface{}{"value": value}}}
	}
	opts, err := m.PrepareClusterObjectSet(ctx, migration.Options{SubscriptionName: name, SubscriptionNamespace: ns.Name})
	if err != nil {
		t.Fatal(err)
	}
	systemNamespace = opts.SystemNamespace
	if !opts.UseClusterObjectDeployment {
		t.Fatal("test requires COD support")
	}
	if err := m.CreateClusterObjectSet(ctx, opts, &migration.MigrationInfo{CollectedObjects: []unstructured.Unstructured{manifest(original.Name, "original")}}); err != nil {
		t.Fatal(err)
	}
	var cod ocv1.ClusterObjectDeployment
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &cod); err != nil {
		t.Fatal(err)
	}
	if cod.Spec.Template.Spec.CollisionProtection != ocv1.CollisionProtectionPrevent {
		t.Fatal("COD is not protected")
	}
	var adopted corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKeyFromObject(original), &adopted); err != nil {
		t.Fatal(err)
	}
	if adopted.UID != original.UID || metav1.GetControllerOf(&adopted) == nil {
		t.Fatal("initial object was not adopted in place")
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &cod); err != nil {
			return err
		}
		before := cod.DeepCopy()
		cod.Spec.Template.Spec.Phases[0].Objects = append(cod.Spec.Template.Spec.Phases[0].Objects, ocv1.ClusterObjectSetObject{Type: ocv1.ObjectTypeInlineManifest, InlineManifest: manifest(foreign.Name, "must-not-be-applied")})
		return c.Patch(ctx, &cod, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	}); err != nil {
		t.Fatal(err)
	}
	if err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		var current ocv1.ClusterObjectDeployment
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &current); err != nil {
			return false, err
		}
		condition := apimeta.FindStatusCondition(current.Status.Conditions, ocv1.ClusterObjectDeploymentTypeProgressing)
		return condition != nil && condition.ObservedGeneration == current.Generation && condition.Status == metav1.ConditionFalse && condition.Reason == ocv1.ClusterObjectDeploymentReasonRevisionObjectCollision, nil
	}); err != nil {
		t.Fatalf("expected collision on newly introduced object: %v", err)
	}
	var preserved corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKeyFromObject(foreign), &preserved); err != nil {
		t.Fatal(err)
	}
	if preserved.UID != foreign.UID || !reflect.DeepEqual(preserved.Data, foreign.Data) || !reflect.DeepEqual(preserved.OwnerReferences, foreign.OwnerReferences) || !reflect.DeepEqual(preserved.Labels, foreign.Labels) {
		t.Fatalf("foreign object was modified: %+v", preserved)
	}
}
