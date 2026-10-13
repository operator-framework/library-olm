//go:build e2e

package e2e

import (
	"context"
	"fmt"
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

// TestBatchSafety uses real API objects and the CLI against either replayed
// fixtures or one live OLMv0 install. Failure-continuation is tested at the
// CLI batch boundary in unit tests.
func TestBatchSafety(t *testing.T) {
	if os.Getenv("E2E_BATCH_TEST") != "true" {
		t.Skip("dedicated batch invocation")
	}
	suite := os.Getenv("E2E_SUITE")
	if suite != "fixture" && suite != "real-operator" {
		t.Fatal("batch safety requires an OLMv0 fixture or live installation")
	}
	namespace, name := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	eligible := []client.ObjectKey{{Namespace: namespace, Name: name}}
	if suite == "real-operator" {
		second := client.ObjectKey{Namespace: os.Getenv("E2E_BATCH_SECOND_NAMESPACE"), Name: os.Getenv("E2E_BATCH_SECOND_OPERATOR")}
		if second.Namespace == "" || second.Name == "" {
			t.Fatal("live batch requires E2E_BATCH_SECOND_OPERATOR and E2E_BATCH_SECOND_NAMESPACE")
		}
		eligible = append(eligible, second)
	}
	_, c, _ := newMigrator(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	t.Cleanup(func() { collectArtifacts(t, namespace) })
	if suite == "real-operator" {
		t.Cleanup(func() { collectArtifacts(t, eligible[1].Namespace) })
		for _, operator := range eligible {
			waitForCompletedInstallPlan(t, operator.Namespace, operator.Name)
		}
	}
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	originals := make([]operatorsv1alpha1.Subscription, len(eligible))
	for i, operator := range eligible {
		if err := c.Get(ctx, operator, &originals[i]); err != nil {
			t.Fatal(err)
		}
	}
	original := &originals[0]
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
			count := 1
			if state == "Eligible" {
				count = len(eligible)
			}
			index := strings.Index(out, fmt.Sprintf("=== %s (%d) ===", state, count))
			if index <= previous {
				t.Fatalf("missing/out-of-order batch state %s:\n%s", state, out)
			}
			previous = index
		}
		for _, operator := range eligible {
			if !strings.Contains(out[previous:], "✓ "+operator.Namespace+"/"+operator.Name+" (") {
				t.Fatalf("eligible section omitted %s/%s:\n%s", operator.Namespace, operator.Name, out)
			}
		}
	}
	for i, operator := range eligible {
		var after operatorsv1alpha1.Subscription
		if err := c.Get(ctx, operator, &after); err != nil {
			t.Fatal(err)
		}
		if after.UID != originals[i].UID || !reflect.DeepEqual(after.Spec, originals[i].Spec) {
			t.Fatalf("batch preview changed %s/%s identity or spec", operator.Namespace, operator.Name)
		}
		// OLMv0 may reconcile live Subscription status while the preview runs.
		if suite == "fixture" && !reflect.DeepEqual(after.Status, originals[i].Status) {
			t.Fatal("batch preview changed fixture Subscription status")
		}
		assertNoMigrationObjects(t, operator.Name)
	}
	run(t, cli, "convert", "--all", "--kubeconfig", os.Getenv("KUBECONFIG"))
	for _, operator := range eligible {
		run(t, "kubectl", "wait", "--for=condition=Installed", "clusterextension/"+operator.Name, "--timeout=10m")
		if out, err := output("kubectl", "get", "subscription/"+operator.Name, "-n", operator.Namespace); err == nil {
			t.Fatalf("converted Subscription %s/%s remains:\n%s", operator.Namespace, operator.Name, out)
		}
	}
	for _, state := range []string{"conflict", "ineligible"} {
		run(t, "kubectl", "get", "subscription/batch-"+state, "-n", namespace)
		out, err := output("kubectl", "get", "clusterobjectsets", "-l", migration.LabelOwnerName+"=batch-"+state, "-o", "name")
		if err != nil || strings.TrimSpace(out) != "" {
			t.Fatalf("noneligible batch operator acquired a COS: %v\n%s", err, out)
		}
	}
	run(t, "kubectl", "get", "clusterextension/batch-conflict", "clusterextension/batch-already")
}
