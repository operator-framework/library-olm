package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

func TestCollectRollbackResources(t *testing.T) {
	for _, mode := range []string{"retained", "missing", "replaced", "claimed", "already collected", "invalid snapshot"} {
		t.Run(mode, func(t *testing.T) {
			role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "catalog-role", UID: "original"}}
			identity := unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]interface{}{"name": role.Name}}}
			data, err := json.Marshal([]rollbackObject{{Identity: identity, UID: role.UID}})
			if err != nil {
				t.Fatal(err)
			}
			sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "source", Annotations: map[string]string{rollbackReleaseSnapshotAnnotation: string(data)}}}
			if mode == "replaced" {
				role.UID = "replacement"
			}
			if mode == "claimed" {
				yes := true
				role.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other", Controller: &yes}}
			}
			if mode == "invalid snapshot" {
				sub.Annotations[rollbackReleaseSnapshotAnnotation] = "{"
			}
			objects := []runtime.Object{sub}
			if mode != "missing" {
				objects = append(objects, role)
			}
			m := migrationTestClient(t, objects...)
			seen := map[string]bool{}
			if mode == "already collected" {
				seen[resourceKey(identity)] = true
			}
			got, err := m.collectRollbackResources(t.Context(), Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}, seen)
			wantError := mode == "replaced" || mode == "claimed" || mode == "invalid snapshot"
			if (err != nil) != wantError {
				t.Fatalf("error = %v", err)
			}
			if mode == "retained" && (len(got) != 1 || got[0].GetUID() != role.UID) {
				t.Fatalf("lost retained catalog RBAC: %+v", got)
			}
			if (mode == "missing" || mode == "already collected") && len(got) != 0 {
				t.Fatal("collected absent or duplicate resource")
			}
		})
	}
}

func TestRollbackInventoryRetriesStatusConflict(t *testing.T) {
	for _, changedSpec := range []bool{false, true} {
		t.Run(fmt.Sprint("changedSpec=", changedSpec), func(t *testing.T) {
			sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "source"}, Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "widgets", CatalogSource: "catalog", CatalogSourceNamespace: "olm"}}
			encoded, err := json.Marshal(sub.Spec)
			if err != nil {
				t.Fatal(err)
			}
			ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "source/sub", MigrationSubscriptionBackupAnnotation: string(encoded)}}}
			m := migrationTestClient(t, sub, ce, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "source"}})
			attempts := 0
			m.Client = interceptor.NewClient(m.Client.(client.WithWatch), interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*operatorsv1alpha1.Subscription); !ok {
					return c.Patch(ctx, obj, patch, opts...)
				}
				attempts++
				if attempts != 1 {
					return c.Patch(ctx, obj, patch, opts...)
				}
				if changedSpec {
					var current operatorsv1alpha1.Subscription
					if err := c.Get(ctx, client.ObjectKeyFromObject(sub), &current); err != nil {
						return err
					}
					current.Spec.Channel = "changed"
					if err := c.Update(ctx, &current); err != nil {
						return err
					}
				}
				return apierrors.NewConflict(schema.GroupResource{Resource: "subscriptions"}, sub.Name, errors.New("concurrent update"))
			}})
			err = m.Rollback(t.Context(), Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true})
			if changedSpec {
				if err == nil {
					t.Fatal("accepted changed Subscription")
				}
				if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(ce), &ocv1.ClusterExtension{}); err != nil {
					t.Fatalf("lost recovery backup: %v", err)
				}
				return
			}
			if err != nil || attempts != 2 {
				t.Fatalf("rollback did not retry conflict: attempts=%d error=%v", attempts, err)
			}
			var restored operatorsv1alpha1.Subscription
			if err := m.Client.Get(t.Context(), client.ObjectKeyFromObject(sub), &restored); err != nil {
				t.Fatal(err)
			}
			if restored.Annotations[rollbackReleaseSnapshotAnnotation] == "" {
				t.Fatal("lost rollback inventory")
			}
		})
	}
}
