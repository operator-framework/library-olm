//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

// TestFixtureBackupDirectory separately validates successful disk backup,
// write-failure continuation, and backup ordering with deletion forbidden by RBAC.
func TestFixtureBackupDirectory(t *testing.T) {
	scenario := os.Getenv("E2E_BACKUP_CASE")
	if scenario == "" {
		t.Skip("dedicated backup-directory fixture invocation")
	}
	if os.Getenv("E2E_SUITE") != "fixture" {
		t.Fatal("backup tests require controller-free OLMv0 fixtures")
	}
	if scenario != "success" && scenario != "write-failure" && scenario != "delete-denied" {
		t.Fatalf("unknown backup case %q", scenario)
	}
	namespace, name := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	t.Cleanup(func() { collectArtifacts(t, namespace) })
	_, c, _ := newMigrator(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	var sub operatorsv1alpha1.Subscription
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &sub); err != nil {
		t.Fatal(err)
	}
	var csv operatorsv1alpha1.ClusterServiceVersion
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sub.Status.InstalledCSV}, &csv); err != nil {
		t.Fatal(err)
	}
	var groups operatorsv1.OperatorGroupList
	if err := c.List(ctx, &groups, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	if len(groups.Items) != 1 {
		t.Fatalf("expected one OperatorGroup, got %d", len(groups.Items))
	}
	var originalPlan operatorsv1alpha1.InstallPlan
	if sub.Status.InstallPlanRef == nil {
		t.Fatal("fixture has no InstallPlan reference")
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sub.Status.InstallPlanRef.Name}, &originalPlan); err != nil {
		t.Fatal(err)
	}
	extraPlan := &operatorsv1alpha1.InstallPlan{ObjectMeta: metav1.ObjectMeta{Name: "backup-associated-plan", Namespace: namespace}, Spec: operatorsv1alpha1.InstallPlanSpec{ClusterServiceVersionNames: []string{csv.Name}, Approval: operatorsv1alpha1.ApprovalAutomatic}}
	unrelated := extraPlan.DeepCopy()
	unrelated.Name = "backup-unrelated-plan"
	unrelated.Spec.ClusterServiceVersionNames = []string{"other.v1"}
	for _, plan := range []*operatorsv1alpha1.InstallPlan{extraPlan, unrelated} {
		if err := c.Create(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "not-created", "backup")
	cli := binary(t, "migrate-operators-v0-to-v1")
	args := []string{"convert", name, "-n", namespace, "--backup", dir, "--kubeconfig", os.Getenv("KUBECONFIG")}
	run(t, cli, append(append([]string{}, args...), "--dry-run")...)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote backup directory: %v", err)
	}
	assertNoMigrationObjects(t, name)
	if scenario == "write-failure" {
		blocked := filepath.Dir(dir)
		if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "delete-denied" {
		args[len(args)-1] = backupReadOnlyKubeconfig(t, c)
	}
	out, err := output(cli, args...)
	switch scenario {
	case "delete-denied":
		if err == nil || !strings.Contains(out, "failed to delete Subscription") || !strings.Contains(out, "forbidden") {
			t.Fatalf("expected refusal at deletion after backup, got %v\n%s", err, out)
		}
		var retained operatorsv1alpha1.Subscription
		if err := c.Get(ctx, client.ObjectKeyFromObject(&sub), &retained); err != nil {
			t.Fatal(err)
		}
		if retained.UID != sub.UID || !reflect.DeepEqual(retained.Spec, sub.Spec) {
			t.Fatal("deletion-denied scenario changed Subscription")
		}
		run(t, "kubectl", "get", "csv/"+csv.Name, "-n", namespace)
		assertNoMigrationObjects(t, name)
	default:
		if err != nil {
			t.Fatalf("backup conversion failed: %v\n%s", err, out)
		}
		run(t, "kubectl", "wait", "--for=condition=Installed", "clusterextension/"+name, "--timeout=10m")
		var ce ocv1.ClusterExtension
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &ce); err != nil {
			t.Fatal(err)
		}
		var restoredSub operatorsv1alpha1.SubscriptionSpec
		var restoredOG operatorsv1.OperatorGroupSpec
		if err := json.Unmarshal([]byte(ce.Annotations[migration.MigrationSubscriptionBackupAnnotation]), &restoredSub); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(ce.Annotations[migration.MigrationOperatorGroupBackupAnnotation]), &restoredOG); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(&restoredSub, sub.Spec) || !reflect.DeepEqual(restoredOG, groups.Items[0].Spec) {
			t.Fatal("disk backup outcome changed authoritative CE annotations")
		}
	}
	if scenario == "write-failure" {
		if !strings.Contains(out, "Backup to disk failed") {
			t.Fatalf("non-fatal disk failure did not warn:\n%s", out)
		}
		return
	}
	for path, expected := range map[string]client.Object{
		"subscription.yaml": &sub, "operatorgroup.yaml": &groups.Items[0], "clusterserviceversion.yaml": &csv,
		"installplans/" + originalPlan.Name + ".yaml": &originalPlan, "installplans/" + extraPlan.Name + ".yaml": extraPlan,
	} {
		assertBackupManifest(t, filepath.Join(dir, path), expected)
	}
	if _, err := os.Stat(filepath.Join(dir, "installplans", unrelated.Name+".yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrelated plan was backed up: %v", err)
	}
}

func assertBackupManifest(t *testing.T, path string, expected client.Object) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]interface{}
	if err := json.Unmarshal(encoded, &original); err != nil {
		t.Fatal(err)
	}
	metadata, ok := document["metadata"].(map[string]interface{})
	if !ok || metadata["name"] != expected.GetName() || metadata["namespace"] != expected.GetNamespace() || metadata["uid"] != string(expected.GetUID()) {
		t.Fatalf("backup identity differs: %s", path)
	}
	if document["apiVersion"] == "" || document["kind"] == "" || !reflect.DeepEqual(document["spec"], original["spec"]) || !reflect.DeepEqual(document["status"], original["status"]) {
		t.Fatalf("backup GVK/spec/status differs from captured source: %s", path)
	}
}

func backupReadOnlyKubeconfig(t *testing.T, c client.Client) string {
	t.Helper()
	const identity = "migration-backup-reader"
	run(t, "kubectl", "delete", "clusterrolebinding/"+identity, "clusterrole/"+identity, "--ignore-not-found")
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: identity}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods/portforward"}, Verbs: []string{"create"}},
	}}
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: identity}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: identity}, Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: identity}}}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, obj := range []client.Object{binding, role} {
			if err := client.IgnoreNotFound(c.Delete(cleanupCtx, obj)); err != nil {
				t.Errorf("delete backup test RBAC: %v", err)
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for _, obj := range []client.Object{role, binding} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	config, err := clientcmd.LoadFromFile(os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	current := config.Contexts[config.CurrentContext]
	if current == nil || config.AuthInfos[current.AuthInfo] == nil {
		t.Fatal("fixture kubeconfig has no current credentials")
	}
	credentials := config.AuthInfos[current.AuthInfo]
	credentials.Impersonate = identity
	credentials.ImpersonateGroups = []string{"system:authenticated"}
	path := filepath.Join(t.TempDir(), "read-only.kubeconfig")
	if err := clientcmd.WriteToFile(*config, path); err != nil {
		t.Fatal(err)
	}
	return path
}
