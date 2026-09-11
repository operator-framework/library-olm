//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
)

// TestEnvironment is deliberately small: it makes both E2E make targets verify
// their supplied cluster before scenario fixtures are introduced. Scenario tests
// select their fixture set through E2E_SUITE and use E2E_ARTIFACTS for diagnostics.
func TestEnvironment(t *testing.T) {
	suite := os.Getenv("E2E_SUITE")
	if suite != "real-operator" {
		t.Fatalf("E2E_SUITE must be real-operator, got %q", suite)
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
	t.Logf("running %s suite against Kubernetes %s", suite, serverVersion.GitVersion)
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
	allChecks, err := output(binary(t, "migrate-operators-v0-to-v1"), "check", "--all", "--kubeconfig", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatalf("check --all failed: %v\n%s", err, allChecks)
	}
	if !strings.Contains(allChecks, namespace+"/"+subscription) {
		t.Fatalf("check --all did not report %s/%s:\n%s", namespace, subscription, allChecks)
	}
	run(t, binary(t, "migrate-operators-v0-to-v1"), "check", subscription, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	run(t, binary(t, "migrate-operators-v0-to-v1"), "convert", subscription, "-n", namespace, "--dry-run", "--kubeconfig", os.Getenv("KUBECONFIG"))
	if _, err := output("kubectl", "get", "clusterextension", subscription); err == nil {
		t.Fatal("convert --dry-run created a ClusterExtension")
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
