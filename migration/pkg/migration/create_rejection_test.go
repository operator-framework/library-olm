package migration

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// TestMigrationCreateRejectionRecovery distinguishes definitive create rejection from ambiguous outcomes before restoring OLMv0.
func TestMigrationCreateRejectionRecovery(t *testing.T) {
	for _, mode := range []string{"COS", "COD"} {
		for _, tc := range []struct {
			name      string
			err       error
			ambiguous bool
		}{
			{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Group: ocv1.GroupVersion.Group, Resource: "revisions"}, "example", errors.New("create denied"))},
			{name: "invalid", err: apierrors.NewInvalid(schema.GroupKind{Group: ocv1.GroupVersion.Group, Kind: mode}, "example", nil)},
			{name: "bad request", err: apierrors.NewBadRequest("rejected")},
			{name: "unauthorized", err: apierrors.NewUnauthorized("rejected")},
			{name: "timeout", err: apierrors.NewTimeoutError("unknown result", 1), ambiguous: true},
			{name: "transport failure", err: errors.New("connection reset"), ambiguous: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				m := migrationTestClient(t)
				if mode == "COD" {
					crd := establishedClusterObjectDeploymentCRD()
					if err := m.Client.Create(ctx, crd); err != nil {
						t.Fatal(err)
					}
				}
				attempted := false
				m.Client = interceptor.NewClient(m.Client.(client.WithWatch), interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						_, isCOS := obj.(*ocv1.ClusterObjectSet)
						_, isCOD := obj.(*ocv1.ClusterObjectDeployment)
						if mode == "COS" && isCOS || mode == "COD" && isCOD {
							attempted = true
							return tc.err
						}
						return c.Create(ctx, obj, opts...)
					},
				})
				m.Client = reconcilingSubscriptionClient{Client: m.Client}
				sub, _ := healthySubscriptionFixtures()
				result, err := m.CreateMigrationResources(ctx, Options{ClusterExtensionName: "example", SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}, &MigrationInfo{}, &Backup{Subscription: sub})
				if !attempted || !errors.Is(err, tc.err) {
					t.Fatalf("expected create rejection %v, got %v (attempted: %v)", tc.err, err, attempted)
				}
				wantActive := tc.ambiguous || mode == "COD"
				if result.TargetMayBeActive != wantActive {
					t.Fatalf("TargetMayBeActive = %v, want %v", result.TargetMayBeActive, wantActive)
				}
				var restored operatorsv1alpha1.Subscription
				getErr := m.Client.Get(ctx, client.ObjectKeyFromObject(sub), &restored)
				if wantActive {
					if !apierrors.IsNotFound(getErr) {
						t.Fatalf("potentially active migration restored OLMv0: %v", getErr)
					}
				} else if getErr != nil || restored.Status.State != operatorsv1alpha1.SubscriptionStateAtLatest {
					t.Fatalf("definitive rejection did not restore OLMv0: %v, state %s", getErr, restored.Status.State)
				}
			})
		}
	}
}
