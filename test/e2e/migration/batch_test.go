//go:build e2e

package e2e

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

// TestFixtureBatchSafety uses real API objects and the CLI; failure-continuation
// is tested deterministically at the CLI batch boundary in unit tests.
func TestFixtureBatchSafety(t *testing.T) {
	if os.Getenv("E2E_BATCH_TEST") != "true" {
		t.Skip("dedicated batch fixture invocation")
	}
	if os.Getenv("E2E_SUITE") != "fixture" {
		t.Fatal("batch safety requires controller-free OLMv0 fixtures")
	}
	namespace, name := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	_, c, _ := newMigrator(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	t.Cleanup(func() { collectArtifacts(t, namespace) })
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	var original operatorsv1alpha1.Subscription
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &original); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"conflict", "ineligible"} {
		sub := &operatorsv1alpha1.Subscription{
			ObjectMeta: metav1.ObjectMeta{Name: "batch-" + state, Namespace: namespace},
			Spec:       original.Spec.DeepCopy(),
		}
		sub.Spec.Package = "migration-batch-" + state
		if state == "ineligible" {
			sub.Annotations = map[string]string{"olm.generated-by": "batch-parent"}
		}
		if err := c.Create(ctx, sub); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			if err := c.Delete(cleanupCtx, sub); err != nil {
				t.Errorf("delete batch Subscription: %v", err)
			}
		})
	}
	for _, state := range []string{"conflict", "already"} {
		ce := &ocv1.ClusterExtension{
			ObjectMeta: metav1.ObjectMeta{Name: "batch-" + state, Annotations: map[string]string{migration.MigratedFromSubscriptionAnnotation: namespace + "/batch-" + state}},
			Spec:       ocv1.ClusterExtensionSpec{Namespace: namespace, Source: ocv1.SourceConfig{SourceType: "Catalog", Catalog: &ocv1.CatalogFilter{PackageName: "migration-batch-" + state}}},
		}
		if err := c.Create(ctx, ce); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			if err := c.Delete(cleanupCtx, ce); err != nil {
				t.Errorf("delete batch CE: %v", err)
			}
		})
	}
	cli := binary(t, "migrate-operators-v0-to-v1")
	for _, args := range [][]string{{"check", "--all"}, {"convert", "--all", "--dry-run"}} {
		out, err := output(cli, append(args, "--kubeconfig", os.Getenv("KUBECONFIG"))...)
		if err != nil {
			t.Fatalf("batch %v failed: %v\n%s", args, err, out)
		}
		previous := -1
		for _, state := range []string{"Conflict", "Ineligible", "AlreadyMigrated", "Eligible"} {
			index := strings.Index(out, "=== "+state+" (1) ===")
			if index <= previous {
				t.Fatalf("missing/out-of-order batch state %s:\n%s", state, out)
			}
			previous = index
		}
	}
	var after operatorsv1alpha1.Subscription
	if err := c.Get(ctx, client.ObjectKeyFromObject(&original), &after); err != nil {
		t.Fatal(err)
	}
	if after.UID != original.UID || !reflect.DeepEqual(after.Spec, original.Spec) || !reflect.DeepEqual(after.Status, original.Status) {
		t.Fatal("batch preview changed source Subscription")
	}
	assertNoMigrationObjects(t, name)
	run(t, cli, "convert", "--all", "--kubeconfig", os.Getenv("KUBECONFIG"))
	run(t, "kubectl", "wait", "--for=condition=Installed", "clusterextension/"+name, "--timeout=10m")
	for _, state := range []string{"conflict", "ineligible"} {
		run(t, "kubectl", "get", "subscription/batch-"+state, "-n", namespace)
		out, err := output("kubectl", "get", "clusterobjectsets", "-l", migration.LabelOwnerName+"=batch-"+state, "-o", "name")
		if err != nil || strings.TrimSpace(out) != "" {
			t.Fatalf("noneligible batch operator acquired a COS: %v\n%s", err, out)
		}
	}
	run(t, "kubectl", "get", "clusterextension/batch-conflict", "clusterextension/batch-already")
}
