//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

// TestEnvironment is deliberately small: it makes both E2E make targets verify
// their supplied cluster before scenario fixtures are introduced. Scenario tests
// select their fixture set through E2E_SUITE and use E2E_ARTIFACTS for diagnostics.
func TestEnvironment(t *testing.T) {
	suite := os.Getenv("E2E_SUITE")
	if suite != "fixture" && suite != "real-operator" && suite != "in-cluster-job" {
		t.Fatalf("E2E_SUITE must be fixture, real-operator, or in-cluster-job, got %q", suite)
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG is required for E2E tests")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("load kubeconfig: %v", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		t.Fatalf("create discovery client: %v", err)
	}
	serverVersion, err := discoveryClient.ServerVersion()
	if err != nil {
		t.Fatalf("connect to E2E cluster: %v", err)
	}
	for _, groupVersion := range []string{"operators.coreos.com/v1alpha1", "olm.operatorframework.io/v1"} {
		if _, err := discoveryClient.ServerResourcesForGroupVersion(groupVersion); err != nil {
			t.Fatalf("required API %s is unavailable: %v", groupVersion, err)
		}
	}
	if suite == "fixture" || suite == "in-cluster-job" {
		out, err := output("kubectl", "get", "deployment/olm-operator", "-n", "olm", "--ignore-not-found", "-o", "name")
		if err != nil {
			t.Fatalf("verify OLMv0 controller absence: %v\n%s", err, out)
		}
		if strings.TrimSpace(out) != "" {
			t.Fatal("fixture suite must not run with the OLMv0 controller installed")
		}
	}
	t.Logf("running %s suite against Kubernetes %s", suite, serverVersion.GitVersion)
}

// TestMigrationInClusterJob proves that both migration CLIs can authenticate
// with projected ServiceAccount credentials rather than a host kubeconfig.
// It uses the controller-free fixture cluster so its OLMv0 installation is
// deterministic; unlike the coverage suites, the Job's process is deliberately
// uninstrumented.
func TestMigrationInClusterJob(t *testing.T) {
	if os.Getenv("E2E_SUITE") != "in-cluster-job" {
		t.Skip("in-cluster migration is exercised by its dedicated target")
	}
	namespace, subscription, image := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION"), os.Getenv("E2E_MIGRATION_IMAGE")
	if namespace == "" || subscription == "" || image == "" {
		t.Fatal("E2E_NAMESPACE, E2E_SUBSCRIPTION, and E2E_MIGRATION_IMAGE are required")
	}
	runnerNamespace := "migration-e2e-job"
	serviceAccount := "migration-runner"
	binding := "migration-e2e-job-admin"
	job := "migration-" + subscription
	// Retain the Job and its namespace for log inspection. The start of the next
	// invocation removes them before creating fresh ones, while cleanup removes
	// the test-only elevated binding as soon as the Job has finished.
	t.Cleanup(func() {
		_, _ = output("kubectl", "delete", "clusterrolebinding/"+binding, "--ignore-not-found")
	})
	run(t, "kubectl", "delete", "clusterrolebinding/"+binding, "--ignore-not-found")
	run(t, "kubectl", "delete", "namespace/"+runnerNamespace, "--ignore-not-found", "--wait=true", "--timeout=10m")
	run(t, "kubectl", "create", "namespace", runnerNamespace)
	run(t, "kubectl", "create", "serviceaccount", serviceAccount, "-n", runnerNamespace)
	run(t, "kubectl", "create", "clusterrolebinding", binding, "--clusterrole=cluster-admin", "--serviceaccount="+runnerNamespace+":"+serviceAccount)

	jobManifest := fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: %s
      containers:
      - name: migrate
        image: %s
        imagePullPolicy: Never
        command: ["/bin/sh", "-ec"]
        args:
        - |
          migrate-catalogs-v0-to-v1
          migrate-operators-v0-to-v1 check %s -n %s
          migrate-operators-v0-to-v1 convert %s -n %s
`, job, runnerNamespace, serviceAccount, image, subscription, namespace, subscription, namespace)
	path := filepath.Join(t.TempDir(), "migration-job.yaml")
	if err := os.WriteFile(path, []byte(jobManifest), 0o600); err != nil {
		t.Fatalf("write migration Job: %v", err)
	}
	run(t, "kubectl", "apply", "-f", path)
	if out, err := output("kubectl", "wait", "--for=condition=Complete", "job/"+job, "-n", runnerNamespace, "--timeout=10m"); err != nil {
		logs, _ := output("kubectl", "logs", "job/"+job, "-n", runnerNamespace)
		t.Fatalf("wait for migration Job: %v\n%s\nJob logs:\n%s", err, out, logs)
	}
	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Installed')].status}=True", "clusterextension/"+subscription, "--timeout=10m")
	if _, err := output("kubectl", "get", "subscription/"+subscription, "-n", namespace); err == nil {
		t.Fatal("Subscription still exists after the in-cluster migration Job completed")
	}
}

// TestCatalogSourceMigration creates a real OLMv0 image CatalogSource, migrates
// it with the catalog CLI, and verifies the OLMv1 ClusterCatalog is serving.
// The bootstrap catalog's resolved digest is deliberately distinct from the
// bootstrap ClusterCatalog's tag reference, so the test exercises creation.
func TestCatalogSourceMigration(t *testing.T) {
	if os.Getenv("E2E_SUITE") != "real-operator" {
		t.Skip("CatalogSource controller is intentionally absent from fixture suites")
	}
	subscription := os.Getenv("E2E_SUBSCRIPTION")
	if subscription == "" {
		t.Fatal("E2E_SUBSCRIPTION is required")
	}
	image, err := output("kubectl", "get", "clustercatalog/operatorhubio", "-o", "jsonpath={.status.resolvedSource.image.ref}")
	if err != nil || strings.TrimSpace(image) == "" {
		t.Fatalf("get bootstrap catalog image: %v (%s)", err, image)
	}
	name := "migration-catalog-" + subscription
	namespace := "migration-e2e-catalog-" + subscription
	t.Cleanup(func() {
		_, _ = output("kubectl", "delete", "catalogsource/"+name, "-n", namespace, "--ignore-not-found", "--wait=true")
		_, _ = output("kubectl", "delete", "clustercatalog/"+name, "--ignore-not-found", "--wait=true")
		_, _ = output("kubectl", "delete", "namespace/"+namespace, "--ignore-not-found", "--wait=true")
	})

	catalogSource := map[string]interface{}{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "CatalogSource",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"sourceType": "grpc",
			"image":      strings.TrimSpace(image),
		},
	}
	data, err := json.Marshal(catalogSource)
	if err != nil {
		t.Fatalf("encode CatalogSource: %v", err)
	}
	path := filepath.Join(t.TempDir(), "catalogsource.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write CatalogSource: %v", err)
	}
	// A previous interrupted test process cannot run t.Cleanup, so clear this
	// deterministic test identity before recreating it.
	run(t, "kubectl", "delete", "clustercatalog/"+name, "--ignore-not-found", "--wait=true")
	run(t, "kubectl", "delete", "namespace/"+namespace, "--ignore-not-found", "--wait=true")
	run(t, "kubectl", "create", "namespace", namespace)
	run(t, "kubectl", "apply", "-f", path)
	existingRefs, err := output("kubectl", "get", "clustercatalogs", "-o", "jsonpath={range .items[*]}{.spec.source.image.ref}{\"\\n\"}{end}")
	if err != nil {
		t.Fatalf("list existing ClusterCatalog image references: %v\\n%s", err, existingRefs)
	}
	if strings.Contains("\n"+existingRefs+"\n", "\n"+strings.TrimSpace(image)+"\n") {
		t.Fatalf("catalog image %q already belongs to a ClusterCatalog; test would exercise adoption instead of creation", strings.TrimSpace(image))
	}
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Serving')].status}=True", "clustercatalog/"+name, "--timeout=10m")

	ref, err := output("kubectl", "get", "clustercatalog/"+name, "-o", "jsonpath={.spec.source.image.ref}")
	if err != nil || strings.TrimSpace(ref) != strings.TrimSpace(image) {
		t.Fatalf("migrated catalog image = %q, want %q (err=%v)", ref, image, err)
	}
	annotation, err := output("kubectl", "get", "clustercatalog/"+name, "-o", "jsonpath={.metadata.annotations.olm\\.operatorframework\\.io/migrated-from-catalogsource}")
	if err != nil || strings.TrimSpace(annotation) != namespace+"/"+name {
		t.Fatalf("migrated-from annotation = %q, want %q (err=%v)", annotation, namespace+"/"+name, err)
	}
	run(t, "kubectl", "get", "catalogsource/"+name, "-n", namespace)
}

// TestFixtureNegativeGuards verifies that fixture migration refuses unsafe
// inputs without creating OLMv1 resources. It runs before TestMigration, which
// recreates the migrated catalog and performs the successful conversion.
func TestFixtureNegativeGuards(t *testing.T) {
	if os.Getenv("E2E_SUITE") != "fixture" {
		t.Skip("negative fixture guards run only against the controller-free OLMv0 fixture suite")
	}
	namespace, subscription := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	if namespace == "" || subscription == "" {
		t.Fatal("E2E_NAMESPACE and E2E_SUBSCRIPTION are required")
	}

	// A non-steady Subscription is unsafe to migrate. Both check and convert
	// must reject it before they remove OLMv0 resources or create OLMv1 ones.
	t.Cleanup(func() {
		if out, err := output("kubectl", "patch", "subscription/"+subscription, "-n", namespace,
			"--subresource=status", "--type=merge", "--patch", `{"status":{"state":"AtLatestKnown"}}`); err != nil {
			t.Errorf("restore Subscription state: %v\n%s", err, out)
		}
	})
	run(t, "kubectl", "patch", "subscription/"+subscription, "-n", namespace,
		"--subresource=status", "--type=merge", "--patch", `{"status":{"state":"UpgradeAvailable"}}`)
	expectCheckFailure(t, "Subscription state", binary(t, "migrate-operators-v0-to-v1"), "check", subscription, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	expectFailure(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	assertNoMigrationObjects(t, subscription)
	run(t, "kubectl", "patch", "subscription/"+subscription, "-n", namespace,
		"--subresource=status", "--type=merge", "--patch", `{"status":{"state":"AtLatestKnown"}}`)
	run(t, "kubectl", "get", "subscription/"+subscription, "-n", namespace)

	// Catalog availability is a hard prerequisite. Ask the serving catalog for a
	// package that does not exist, then verify the failed resolution is still
	// non-mutating. Unlike deleting ClusterCatalogs, this does not race the
	// bootstrap catalog reconciler.
	packageName, err := output("kubectl", "get", "subscription/"+subscription, "-n", namespace, "-o", "jsonpath={.spec.name}")
	if err != nil || strings.TrimSpace(packageName) == "" {
		t.Fatalf("get source package: %v (%s)", err, packageName)
	}
	t.Cleanup(func() {
		patch := fmt.Sprintf(`{"spec":{"name":%q}}`, strings.TrimSpace(packageName))
		if out, err := output("kubectl", "patch", "subscription/"+subscription, "-n", namespace,
			"--type=merge", "--patch", patch); err != nil {
			t.Errorf("restore Subscription package: %v\n%s", err, out)
		}
	})
	run(t, "kubectl", "patch", "subscription/"+subscription, "-n", namespace,
		"--type=merge", "--patch", `{"spec":{"name":"migration-fixture-package-that-does-not-exist"}}`)
	expectCheckFailure(t, "No ClusterCatalog found", binary(t, "migrate-operators-v0-to-v1"), "check", subscription, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	expectFailure(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	assertNoMigrationObjects(t, subscription)
	run(t, "kubectl", "patch", "subscription/"+subscription, "-n", namespace,
		"--type=merge", "--patch", fmt.Sprintf(`{"spec":{"name":%q}}`, strings.TrimSpace(packageName)))
	run(t, "kubectl", "get", "subscription/"+subscription, "-n", namespace)
}

// TestPrecreatedClusterObjectSetSupersession verifies the released controller's
// migration handoff: the migration-created COS uses collision protection None,
// then the catalog creates a controller-owned revision with Prevent protection.
// The catalog revision is newer and therefore supersedes the migration revision.
// It is intentionally a dedicated invocation because it mutates its fixture
// installation without exercising the CLI's complete cleanup path.
func TestPrecreatedClusterObjectSetSupersession(t *testing.T) {
	if os.Getenv("E2E_COS_SUPERSESSION_TEST") != "true" {
		t.Skip("set E2E_COS_SUPERSESSION_TEST=true to run the ClusterObjectSet supersession proof")
	}
	namespace, subscription := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	if namespace == "" || subscription == "" {
		t.Fatal("E2E_NAMESPACE and E2E_SUBSCRIPTION are required")
	}
	t.Cleanup(func() { collectArtifacts(t, namespace) })

	ctx := context.Background()
	m, kubeClient, restConfig := newMigrator(t)
	opts := migration.Options{SubscriptionName: subscription, SubscriptionNamespace: namespace}
	opts.ApplyDefaults()

	// C7 must be satisfied before CE construction. The fixture target installs
	// a CatalogSource and the catalog CLI creates its ClusterCatalog.
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	_, csv, installPlan, err := m.GetCSVAndInstallPlan(ctx, opts)
	if err != nil {
		t.Fatalf("profile OLMv0 installation: %v", err)
	}
	info, err := m.GetBundleInfo(ctx, opts, csv, installPlan)
	if err != nil {
		t.Fatalf("get bundle information: %v", err)
	}
	catalogName, err := m.ResolveClusterCatalog(ctx, info, restConfig)
	if err != nil {
		t.Fatalf("resolve ClusterCatalog: %v", err)
	}
	if catalogName != "operatorhubio-catalog" {
		t.Fatalf("resolved ClusterCatalog = %q, want fixture catalog %q", catalogName, "operatorhubio-catalog")
	}
	info.ResolvedCatalogName = catalogName
	info.CollectedObjects, err = m.CollectResources(ctx, opts, csv, installPlan, info.PackageName)
	if err != nil {
		t.Fatalf("collect migration resources: %v", err)
	}

	if err := m.PrepareForMigration(ctx, opts, csv); err != nil {
		t.Fatalf("remove OLMv0 management before creating COS: %v", err)
	}
	if err := m.CreateClusterObjectSet(ctx, opts, info); err != nil {
		t.Fatalf("create pre-existing COS: %v", err)
	}

	var before ocv1.ClusterObjectSetList
	if err := kubeClient.List(ctx, &before, client.MatchingLabels{
		migration.LabelOwnerKind: ocv1.ClusterExtensionKind,
		migration.LabelOwnerName: subscription,
	}); err != nil {
		t.Fatalf("list pre-existing COS: %v", err)
	}
	if len(before.Items) != 1 {
		t.Fatalf("pre-existing COS count = %d, want 1", len(before.Items))
	}
	precreatedUID := before.Items[0].UID
	if precreatedUID == "" {
		t.Fatal("pre-existing COS has no UID")
	}
	if !cosSucceeded(before.Items[0]) {
		t.Fatal("pre-existing COS did not reach Succeeded=True before CE creation")
	}
	if before.Items[0].Spec.CollisionProtection != ocv1.CollisionProtectionNone {
		t.Fatalf("migration COS collisionProtection = %q, want %q", before.Items[0].Spec.CollisionProtection, ocv1.CollisionProtectionNone)
	}

	if err := m.CreateClusterExtension(ctx, opts, info); err != nil {
		t.Fatalf("create CE for pre-existing COS: %v", err)
	}
	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Installed')].status}=True", "clusterextension/"+subscription, "--timeout=10m")
	var after ocv1.ClusterObjectSetList
	if err := kubeClient.List(ctx, &after, client.MatchingLabels{
		migration.LabelOwnerKind: ocv1.ClusterExtensionKind,
		migration.LabelOwnerName: subscription,
	}); err != nil {
		t.Fatalf("list COS after CE creation: %v", err)
	}
	if len(after.Items) != 2 {
		t.Fatalf("COS count after CE creation = %d, want 2 (migration and catalog revisions)", len(after.Items))
	}
	var migrationCOS, catalogCOS *ocv1.ClusterObjectSet
	for i := range after.Items {
		cos := &after.Items[i]
		switch cos.Spec.Revision {
		case 1:
			migrationCOS = cos
		case 2:
			catalogCOS = cos
		}
	}
	if migrationCOS == nil || catalogCOS == nil {
		t.Fatalf("COS revisions = %#v, want migration revision 1 and catalog revision 2", after.Items)
	}
	if migrationCOS.UID != precreatedUID {
		t.Fatalf("migration COS UID = %s, want pre-created UID %s", migrationCOS.UID, precreatedUID)
	}
	if migrationCOS.Spec.CollisionProtection != ocv1.CollisionProtectionNone {
		t.Fatalf("migration COS collisionProtection = %q, want %q", migrationCOS.Spec.CollisionProtection, ocv1.CollisionProtectionNone)
	}
	if catalogCOS.Spec.CollisionProtection != ocv1.CollisionProtectionPrevent {
		t.Fatalf("catalog COS collisionProtection = %q, want %q", catalogCOS.Spec.CollisionProtection, ocv1.CollisionProtectionPrevent)
	}
	if len(catalogCOS.OwnerReferences) != 1 || catalogCOS.OwnerReferences[0].Name != subscription {
		t.Fatalf("catalog COS ownerReferences = %#v, want ClusterExtension %q", catalogCOS.OwnerReferences, subscription)
	}
	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Succeeded')].status}=True", "clusterobjectset/"+catalogCOS.Name, "--timeout=10m")
}

func cosSucceeded(cos ocv1.ClusterObjectSet) bool {
	for _, condition := range cos.Status.Conditions {
		if condition.Type == ocv1.ClusterObjectSetTypeSucceeded && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func newMigrator(t *testing.T) (*migration.Migrator, client.Client, *rest.Config) {
	t.Helper()
	config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatalf("load kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ocv1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(operatorsv1.AddToScheme(scheme))
	utilruntime.Must(operatorsv1alpha1.AddToScheme(scheme))
	kubeClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("create Kubernetes client: %v", err)
	}
	return migration.NewMigrator(kubeClient, config), kubeClient, config
}

// TestMigration applies the suite's complete fixture, exercises the two migration
// binaries, and observes the API server rather than mocking either OLM controller.
// E2E_MANIFEST must create the namespace, a CatalogSource, and the named Subscription.
func TestMigration(t *testing.T) {
	manifest, namespace, subscription := os.Getenv("E2E_MANIFEST"), os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	if namespace == "" || subscription == "" {
		t.Fatal("E2E_NAMESPACE and E2E_SUBSCRIPTION are required")
	}
	t.Cleanup(func() { collectArtifacts(t, namespace) })
	if manifest != "" {
		run(t, "kubectl", "apply", "-f", manifest)
	}
	run(t, "kubectl", "wait", "--for=jsonpath={.status.state}=AtLatestKnown", "subscription/"+subscription, "-n", namespace, "--timeout=10m")
	csvName, err := output("kubectl", "get", "subscription/"+subscription, "-n", namespace, "-o", "jsonpath={.status.installedCSV}")
	if err != nil || strings.TrimSpace(csvName) == "" {
		t.Fatalf("get installed CSV for Subscription %s: %v (%s)", subscription, err, csvName)
	}
	run(t, "kubectl", "wait", "--for=jsonpath={.status.phase}=Succeeded", "csv/"+strings.TrimSpace(csvName), "-n", namespace, "--timeout=10m")
	subscriptionJSON, err := output("kubectl", "get", "subscription/"+subscription, "-n", namespace, "-o", "json")
	if err != nil {
		t.Fatalf("capture source Subscription for conflict cleanup: %v\n%s", err, subscriptionJSON)
	}

	// Catalog migration is deliberately run before the operator check: C7 is a
	// hard prerequisite and this verifies the prescribed command sequence.
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	if os.Getenv("E2E_SUITE") == "real-operator" {
		allChecks, err := output(binary(t, "migrate-operators-v0-to-v1"), "check", "--all", "--kubeconfig", os.Getenv("KUBECONFIG"))
		if err != nil {
			t.Fatalf("check --all failed: %v\n%s", err, allChecks)
		}
		if !strings.Contains(allChecks, namespace+"/"+subscription) {
			t.Fatalf("check --all did not report %s/%s:\n%s", namespace, subscription, allChecks)
		}
	}
	run(t, binary(t, "migrate-operators-v0-to-v1"), "check", subscription, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	if os.Getenv("E2E_SUITE") == "real-operator" {
		run(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription, "-n", namespace, "--dry-run", "--kubeconfig", os.Getenv("KUBECONFIG"))
		if _, err := output("kubectl", "get", "clusterextension", subscription); err == nil {
			t.Fatal("convert --dry-run created a ClusterExtension")
		}
	}
	run(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))

	// The ClusterExtension name defaults to the Subscription name. Installed=True
	// proves the live operator-controller accepted the generated COS and rendered it.
	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Installed')].status}=True", "clusterextension/"+subscription, "--timeout=10m")
	cos, err := output("kubectl", "get", "clusterobjectsets", "-l", "olm.operatorframework.io/owner-name="+subscription, "-o", "name")
	if err != nil || strings.TrimSpace(cos) == "" {
		t.Fatalf("find ClusterObjectSet owned by %s: %v (%s)", subscription, err, cos)
	}
	if _, err := output("kubectl", "get", "subscription", subscription, "-n", namespace); err == nil {
		t.Fatal("Subscription still exists after successful conversion")
	}
	if os.Getenv("E2E_SUITE") == "real-operator" {
		restoreSubscriptionForConflict(t, subscriptionJSON)
		run(t, binary(t, "migrate-operators-v0-to-v1"), "cleanup", subscription, "--kubeconfig", os.Getenv("KUBECONFIG"))
		run(t, "kubectl", "get", "clusterextension", subscription)
		if _, err := output("kubectl", "get", "subscription", subscription, "-n", namespace); err == nil {
			t.Fatal("cleanup left the conflict Subscription in place")
		}
		if _, err := output(binary(t, "migrate-operators-v0-to-v1"), "rollback", subscription, "--kubeconfig", os.Getenv("KUBECONFIG")); err == nil {
			t.Fatal("rollback of an installed ClusterExtension succeeded without acknowledgment")
		}
		run(t, binary(t, "migrate-operators-v0-to-v1"), "rollback", subscription, "--acknowledge-installed", "--kubeconfig", os.Getenv("KUBECONFIG"))
		run(t, "kubectl", "get", "subscription", subscription, "-n", namespace)
	}
}

// TestCrossNamespaceMigration proves the explicit install-namespace path using
// a committed OLMv0 fixture. It is a separate invocation because it leaves the
// source namespace in place long enough to verify that only migrated operator
// resources, rather than the whole namespace, were removed.
func TestCrossNamespaceMigration(t *testing.T) {
	if os.Getenv("E2E_CROSS_NAMESPACE_TEST") != "true" {
		t.Skip("set E2E_CROSS_NAMESPACE_TEST=true to run the cross-namespace migration scenario")
	}
	if os.Getenv("E2E_SUITE") != "fixture" {
		t.Fatal("cross-namespace migration is exercised against the fixture suite")
	}

	namespace, subscription := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	if namespace == "" || subscription == "" {
		t.Fatal("E2E_NAMESPACE and E2E_SUBSCRIPTION are required")
	}
	targetNamespace := namespace + "-target"
	t.Cleanup(func() {
		collectArtifacts(t, namespace)
		if t.Failed() {
			collectArtifacts(t, targetNamespace)
		}
	})

	// Use labels which must be copied before the controller renders the target
	// bundle. The target namespace is deliberately absent at conversion start.
	run(t, "kubectl", "delete", "namespace/"+targetNamespace, "--ignore-not-found", "--wait=true")
	// Use audit rather than enforce: the fixture bundle is not restricted-PSA
	// compliant, so enforce would correctly prevent its Deployment from being
	// created and turn this namespace-label preservation test into an unrelated
	// admission test.
	run(t, "kubectl", "label", "namespace/"+namespace,
		"pod-security.kubernetes.io/audit=restricted",
		"security.openshift.io/scc.podSecurityLabelSync=true", "--overwrite")

	sourceDeployments, err := output("kubectl", "get", "deployment", "-n", namespace, "-o", "name")
	if err != nil || strings.TrimSpace(sourceDeployments) == "" {
		t.Fatalf("list source operator deployments: %v\n%s", err, sourceDeployments)
	}

	// The fixture CatalogSource is intentionally present but not reconciled by
	// OLMv0; create its ClusterCatalog before conversion to satisfy C7.
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	run(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription,
		"-n", namespace, "--install-namespace", targetNamespace,
		"--kubeconfig", os.Getenv("KUBECONFIG"))

	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Installed')].status}=True", "clusterextension/"+subscription, "--timeout=10m")
	gotNamespace, err := output("kubectl", "get", "clusterextension/"+subscription, "-o", "jsonpath={.spec.namespace}")
	if err != nil || strings.TrimSpace(gotNamespace) != targetNamespace {
		t.Fatalf("ClusterExtension install namespace = %q, err=%v; want %q", gotNamespace, err, targetNamespace)
	}
	for key, want := range map[string]string{
		"pod-security.kubernetes.io/audit":               "restricted",
		"security.openshift.io/scc.podSecurityLabelSync": "true",
	} {
		got, err := output("kubectl", "get", "namespace/"+targetNamespace, "-o", "jsonpath={.metadata.labels."+escapeJSONPathLabel(key)+"}")
		if err != nil || strings.TrimSpace(got) != want {
			t.Fatalf("target namespace label %s = %q, err=%v; want %q", key, got, err, want)
		}
	}

	// The catalog-rendered target Deployment proves the new installation is
	// present. The source Deployments must be gone: retaining them would leave
	// two active operator copies when the source namespace is intentionally kept.
	targetDeployments, err := output("kubectl", "get", "deployment", "-n", targetNamespace, "-o", "name")
	if err != nil || strings.TrimSpace(targetDeployments) == "" {
		t.Fatalf("list target operator deployments: %v\n%s", err, targetDeployments)
	}
	for _, deployment := range strings.Fields(sourceDeployments) {
		if out, err := output("kubectl", "get", deployment, "-n", namespace); err == nil {
			t.Fatalf("source operator resource %s remains after cross-namespace migration:\n%s", deployment, out)
		}
	}
	sourceDeletionTimestamp, err := output("kubectl", "get", "namespace/"+namespace, "-o", "jsonpath={.metadata.deletionTimestamp}")
	if err != nil || strings.TrimSpace(sourceDeletionTimestamp) != "" {
		t.Fatalf("source namespace deletionTimestamp = %q, err=%v; want empty", sourceDeletionTimestamp, err)
	}
}

// TestSystemManagedNamespaceMigration verifies the experimental OLMv1 mode in
// which migration intentionally omits ClusterExtension.spec.namespace. The
// controller manages the bundle's namespace; migration prepares the same
// metadata-derived name before its COS relocates collected OLMv0 objects.
func TestSystemManagedNamespaceMigration(t *testing.T) {
	if os.Getenv("E2E_SYSTEM_MANAGED_NAMESPACE_TEST") != "true" {
		t.Skip("set E2E_SYSTEM_MANAGED_NAMESPACE_TEST=true to run the system-managed namespace scenario")
	}
	if os.Getenv("E2E_SUITE") != "fixture" {
		t.Fatal("system-managed namespace migration is exercised against the fixture suite")
	}

	namespace, subscription := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	if namespace == "" || subscription == "" {
		t.Fatal("E2E_NAMESPACE and E2E_SUBSCRIPTION are required")
	}
	// The fixture bundle supplies operatorframework.io/suggested-namespace,
	// which has precedence over the package-derived default.
	targetNamespace := subscription
	t.Cleanup(func() {
		collectArtifacts(t, namespace)
		if t.Failed() {
			collectArtifacts(t, targetNamespace)
		}
	})

	run(t, "kubectl", "delete", "namespace/"+targetNamespace, "--ignore-not-found", "--wait=true")
	sourceDeployments, err := output("kubectl", "get", "deployment", "-n", namespace, "-o", "name")
	if err != nil || strings.TrimSpace(sourceDeployments) == "" {
		t.Fatalf("list source operator deployments: %v\n%s", err, sourceDeployments)
	}

	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	run(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription,
		"-n", namespace, "--system-managed-install-namespace", "--kubeconfig", os.Getenv("KUBECONFIG"))

	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Installed')].status}=True", "clusterextension/"+subscription, "--timeout=10m")
	ceJSON, err := output("kubectl", "get", "clusterextension/"+subscription, "-o", "json")
	if err != nil {
		t.Fatalf("get ClusterExtension: %v\n%s", err, ceJSON)
	}
	var ce map[string]any
	if err := json.Unmarshal([]byte(ceJSON), &ce); err != nil {
		t.Fatalf("decode ClusterExtension: %v", err)
	}
	spec, ok := ce["spec"].(map[string]any)
	if !ok {
		t.Fatalf("ClusterExtension has no spec: %s", ceJSON)
	}
	if _, found := spec["namespace"]; found {
		t.Fatalf("ClusterExtension unexpectedly sets spec.namespace: %s", ceJSON)
	}

	run(t, "kubectl", "get", "namespace/"+targetNamespace)
	targetDeployments, err := output("kubectl", "get", "deployment", "-n", targetNamespace, "-o", "name")
	if err != nil || strings.TrimSpace(targetDeployments) == "" {
		t.Fatalf("list system-managed target deployments: %v\n%s", err, targetDeployments)
	}
	for _, deployment := range strings.Fields(sourceDeployments) {
		if out, err := output("kubectl", "get", deployment, "-n", namespace); err == nil {
			t.Fatalf("source operator resource %s remains after system-managed migration:\n%s", deployment, out)
		}
	}
	deletionTimestamp, err := output("kubectl", "get", "namespace/"+namespace, "-o", "jsonpath={.metadata.deletionTimestamp}")
	if err != nil || strings.TrimSpace(deletionTimestamp) != "" {
		t.Fatalf("source namespace deletionTimestamp = %q, err=%v; want empty", deletionTimestamp, err)
	}
}

// TestLiveCrossNamespaceDeletionMigration verifies the destructive namespace
// path against OLMv0 itself. Unlike fixture tests, OLMv0 is present to release
// the CSV cleanup finalizer, so Kubernetes can complete namespace deletion.
func TestLiveCrossNamespaceDeletionMigration(t *testing.T) {
	if os.Getenv("E2E_LIVE_NAMESPACE_DELETE_TEST") != "true" {
		t.Skip("set E2E_LIVE_NAMESPACE_DELETE_TEST=true to run live namespace deletion")
	}
	if os.Getenv("E2E_SUITE") != "real-operator" {
		t.Fatal("acknowledged namespace deletion is exercised against live OLMv0")
	}

	namespace, subscription := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	if namespace == "" || subscription == "" {
		t.Fatal("E2E_NAMESPACE and E2E_SUBSCRIPTION are required")
	}
	targetNamespace := namespace + "-target"
	t.Cleanup(func() {
		collectArtifacts(t, namespace)
		if t.Failed() {
			collectArtifacts(t, targetNamespace)
		}
	})

	run(t, "kubectl", "delete", "namespace/"+targetNamespace, "--ignore-not-found", "--wait=true")
	// Audit mode proves PSA labels are transferred without turning this test
	// into an admission-policy test for the catalog bundle.
	run(t, "kubectl", "label", "namespace/"+namespace,
		"pod-security.kubernetes.io/audit=restricted",
		"security.openshift.io/scc.podSecurityLabelSync=true", "--overwrite")

	// Migrate catalogs before converting the Subscription so C7 is satisfied.
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	run(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription,
		"-n", namespace, "--install-namespace", targetNamespace,
		"--acknowledge-namespace-delete", "--kubeconfig", os.Getenv("KUBECONFIG"))

	run(t, "kubectl", "wait", "--for=jsonpath={.status.conditions[?(@.type=='Installed')].status}=True", "clusterextension/"+subscription, "--timeout=10m")
	for key, want := range map[string]string{
		"pod-security.kubernetes.io/audit":               "restricted",
		"security.openshift.io/scc.podSecurityLabelSync": "true",
	} {
		got, err := output("kubectl", "get", "namespace/"+targetNamespace, "-o", "jsonpath={.metadata.labels."+escapeJSONPathLabel(key)+"}")
		if err != nil || strings.TrimSpace(got) != want {
			t.Fatalf("target namespace label %s = %q, err=%v; want %q", key, got, err, want)
		}
	}
	targetDeployments, err := output("kubectl", "get", "deployment", "-n", targetNamespace, "-o", "name")
	if err != nil || strings.TrimSpace(targetDeployments) == "" {
		t.Fatalf("list target operator deployments: %v\n%s", err, targetDeployments)
	}
	if out, err := output("kubectl", "wait", "--for=delete", "namespace/"+namespace, "--timeout=10m"); err != nil {
		t.Fatalf("wait for acknowledged source namespace deletion: %v\n%s", err, out)
	}
}

func escapeJSONPathLabel(label string) string {
	return strings.ReplaceAll(label, ".", `\.`)
}

// restoreSubscriptionForConflict replays the pre-migration Subscription without
// its API-assigned state, creating the Conflict state exercised by cleanup.
func restoreSubscriptionForConflict(t *testing.T, raw string) {
	t.Helper()
	var subscription map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &subscription); err != nil {
		t.Fatalf("decode captured Subscription: %v", err)
	}
	delete(subscription, "status")
	metadata, ok := subscription["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("captured Subscription has no metadata")
	}
	for _, field := range []string{"creationTimestamp", "generation", "managedFields", "resourceVersion", "uid"} {
		delete(metadata, field)
	}
	data, err := json.Marshal(subscription)
	if err != nil {
		t.Fatalf("encode conflict Subscription: %v", err)
	}
	path := filepath.Join(t.TempDir(), "subscription.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write conflict Subscription: %v", err)
	}
	run(t, "kubectl", "apply", "-f", path)
}

// binary returns a verified path to a migration CLI built by the Make target.
func binary(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "bin", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("migration binary %s: %v", path, err)
	}
	return path
}

// run fails the current test with the command's combined output on error.
func run(t *testing.T, command string, args ...string) {
	t.Helper()
	if out, err := output(command, args...); err != nil {
		t.Fatalf("%s %s failed: %v\n%s", command, strings.Join(args, " "), err, out)
	}
}

// expectFailure requires a command to reject its input and includes its output
// in the test failure to make an accidental success diagnosable.
func expectFailure(t *testing.T, command string, args ...string) {
	t.Helper()
	if out, err := output(command, args...); err == nil {
		t.Fatalf("%s %s unexpectedly succeeded:\n%s", command, strings.Join(args, " "), out)
	}
}

// expectCheckFailure verifies the CLI's deliberate check contract: it returns
// success after reporting failed prerequisites, allowing callers to inspect the
// full report. Conversion itself must still reject those prerequisites.
func expectCheckFailure(t *testing.T, want, command string, args ...string) {
	t.Helper()
	out, err := output(command, args...)
	if err != nil {
		t.Fatalf("%s %s returned an error instead of a check report: %v\n%s", command, strings.Join(args, " "), err, out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("%s %s did not report %q:\n%s", command, strings.Join(args, " "), want, out)
	}
}

// assertNoMigrationObjects proves a rejected conversion did not create either
// OLMv1 resource that would take ownership of the OLMv0 installation.
func assertNoMigrationObjects(t *testing.T, subscription string) {
	t.Helper()
	if out, err := output("kubectl", "get", "clusterextension/"+subscription); err == nil {
		t.Fatalf("rejected conversion created ClusterExtension %s:\n%s", subscription, out)
	}
	if out, err := output("kubectl", "get", "clusterobjectsets", "-l", "olm.operatorframework.io/owner-name="+subscription, "-o", "name"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("rejected conversion created ClusterObjectSet(s): err=%v\n%s", err, out)
	}
}

// output runs a command and returns its combined standard output and error.
func output(command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// collectArtifacts saves cluster diagnostics when the current test has failed.
func collectArtifacts(t *testing.T, namespace string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	dir := os.Getenv("E2E_ARTIFACTS")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Logf("create artifacts dir: %v", err)
		return
	}
	for _, resource := range [][]string{
		{"get", "all", "-n", namespace, "-o", "yaml"},
		{"get", "events", "-n", namespace, "-o", "yaml"},
		{"get", "clusterextensions,clusterobjectsets,clustercatalogs", "-o", "yaml"},
		{"get", "events", "-n", "olmv1-system", "-o", "yaml"},
		{"logs", "deployment/catalogd-controller-manager", "-n", "olmv1-system", "--all-containers", "--tail=-1"},
		{"logs", "deployment/operator-controller-controller-manager", "-n", "olmv1-system", "--all-containers", "--tail=-1"},
	} {
		out, _ := output("kubectl", resource...)
		name := strings.NewReplacer(",", "-", "/", "-").Replace(strings.Join(resource[:2], "-"))
		for i, arg := range resource[:len(resource)-1] {
			if arg == "-n" {
				name += "-" + resource[i+1]
				break
			}
		}
		name += ".yaml"
		_ = os.WriteFile(filepath.Join(dir, name), []byte(out), 0o600)
	}
}
