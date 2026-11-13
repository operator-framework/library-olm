package migration

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// TestRevisionServerDryRun checks the real candidate and strict non-persisting
// request options, and preserves server validation and authorization failures.
func TestRevisionServerDryRun(t *testing.T) {
	rejection := errors.New("API server rejected candidate")
	for _, deployment := range []bool{true} {
		for _, serverErr := range []error{nil, rejection} {
			name := "COS"
			if deployment {
				name = "COD"
			}
			if serverErr != nil {
				name += "/rejected"
			}
			t.Run(name, func(t *testing.T) {
				m := migrationTestClient(t)
				calls := 0
				m.Client = interceptor.NewClient(m.Client.(client.WithWatch), interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, options ...client.CreateOption) error {
						calls++
						opts := (&client.CreateOptions{}).ApplyOptions(options)
						if !reflect.DeepEqual(opts.DryRun, []string{metav1.DryRunAll}) || opts.FieldValidation != metav1.FieldValidationStrict {
							t.Fatalf("unsafe create options: %+v", opts)
						}
						var phases []ocv1.ClusterObjectSetPhase
						switch candidate := obj.(type) {
						case *ocv1.ClusterObjectSet:
							if deployment || candidate.Name != "example-1" {
								t.Fatalf("unexpected COS: %+v", candidate)
							}
							phases = candidate.Spec.Phases
						case *ocv1.ClusterObjectDeployment:
							if !deployment || candidate.Name != "example" {
								t.Fatalf("unexpected COD: %+v", candidate)
							}
							phases = candidate.Spec.Template.Spec.Phases
						default:
							t.Fatalf("unexpected write of %T", obj)
						}
						if len(phases) != 1 || len(phases[0].Objects) != 1 {
							t.Fatalf("phases = %+v", phases)
						}
						object := phases[0].Objects[0]
						if object.Type != ocv1.ObjectTypeSecretReference || object.SecretRef.Namespace != "controller" || object.SecretRef.Key == "" {
							t.Fatalf("unpacked candidate: %+v", object)
						}
						return serverErr
					},
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						t.Fatal("dry run deleted an object")
						return nil
					},
				})
				opts := Options{ClusterExtensionName: "example", SystemNamespace: "controller"}
				info := &MigrationInfo{CollectedObjects: []unstructured.Unstructured{{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "config", "namespace": "operators"}, "data": map[string]interface{}{"key": "value"}}}}}
				err := m.validateMigrationRevision(t.Context(), opts, info)
				if !errors.Is(err, serverErr) || calls != 1 {
					t.Fatalf("calls=%d, error=%v", calls, err)
				}
				var secrets corev1.SecretList
				if err := m.Client.List(t.Context(), &secrets); err != nil || len(secrets.Items) != 0 {
					t.Fatalf("dry run persisted Secrets: %v, %+v", err, secrets.Items)
				}
			})
		}
	}
}
