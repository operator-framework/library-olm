package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
)

func diskBackupFixture() *Backup {
	return &Backup{
		Subscription:          &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Namespace: "operators"}, Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "widgets", Channel: "stable", CatalogSource: "catalog", CatalogSourceNamespace: "olm", InstallPlanApproval: operatorsv1alpha1.ApprovalManual}},
		ClusterServiceVersion: &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "operators"}},
		OperatorGroup:         &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "operators", Namespace: "operators"}, Spec: operatorsv1.OperatorGroupSpec{TargetNamespaces: []string{"operators"}}},
		InstallPlan:           &operatorsv1alpha1.InstallPlan{ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "operators"}, Spec: operatorsv1alpha1.InstallPlanSpec{ClusterServiceVersionNames: []string{"widgets.v1"}}},
	}
}

func TestBackupDirectoryManifestsAndIsolation(t *testing.T) {
	b := diskBackupFixture()
	previous := b.InstallPlan.DeepCopy()
	previous.Name = "previous"
	b.InstallPlans = []*operatorsv1alpha1.InstallPlan{previous, b.InstallPlan.DeepCopy(), nil}
	original := diskBackupFixture()
	original.InstallPlans = []*operatorsv1alpha1.InstallPlan{previous.DeepCopy(), original.InstallPlan.DeepCopy(), nil}
	dir := filepath.Join(t.TempDir(), "missing", "backup")
	if err := b.SaveToDisk(dir); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b, original) {
		t.Fatal("disk serialization changed the recovery backup")
	}
	for path, expected := range map[string]struct {
		object     client.Object
		apiVersion string
		kind       string
	}{
		"subscription.yaml":          {b.Subscription, "operators.coreos.com/v1alpha1", "Subscription"},
		"operatorgroup.yaml":         {b.OperatorGroup, "operators.coreos.com/v1", "OperatorGroup"},
		"clusterserviceversion.yaml": {b.ClusterServiceVersion, "operators.coreos.com/v1alpha1", "ClusterServiceVersion"},
		"installplans/current.yaml":  {b.InstallPlan, "operators.coreos.com/v1alpha1", "InstallPlan"},
		"installplans/previous.yaml": {previous, "operators.coreos.com/v1alpha1", "InstallPlan"},
	} {
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]interface{}
		if err := yaml.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if document["apiVersion"] != expected.apiVersion || document["kind"] != expected.kind {
			t.Fatalf("manifest %s has GVK %v/%v, want %s/%s", path, document["apiVersion"], document["kind"], expected.apiVersion, expected.kind)
		}
		metadata := document["metadata"].(map[string]interface{})
		if metadata["name"] != expected.object.GetName() || metadata["namespace"] != expected.object.GetNamespace() {
			t.Fatalf("wrong backup identity in %s", path)
		}
		stat, err := os.Stat(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0o600 {
			t.Fatalf("backup permissions = %o, want 600", stat.Mode().Perm())
		}
	}
	var restored operatorsv1alpha1.Subscription
	data, err := os.ReadFile(filepath.Join(dir, "subscription.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Spec, b.Subscription.Spec) {
		t.Fatal("disk backup lost Subscription spec")
	}
	files, err := os.ReadDir(filepath.Join(dir, "installplans"))
	if err != nil || len(files) != 2 {
		t.Fatalf("duplicate or missing plan files: %v, %v", files, err)
	}
}

func TestBackupDirectoryKeepsSameNamePlansFromDifferentNamespaces(t *testing.T) {
	b := diskBackupFixture()
	b.InstallPlan.Namespace = "other"
	sourcePlan := b.InstallPlan.DeepCopy()
	sourcePlan.Namespace = "operators"
	sourcePlan.Spec.ClusterServiceVersionNames = []string{"source.v1"}
	duplicate := b.InstallPlan.DeepCopy()
	duplicate.Spec.ClusterServiceVersionNames = []string{"stale.v1"}
	previous := b.InstallPlan.DeepCopy()
	previous.Name = "previous"
	b.InstallPlans = []*operatorsv1alpha1.InstallPlan{sourcePlan, duplicate, previous}

	dir := t.TempDir()
	if err := b.SaveToDisk(dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]*operatorsv1alpha1.InstallPlan{
		"installplans/operators/current.yaml": sourcePlan,
		"installplans/other/current.yaml":     b.InstallPlan,
		"installplans/previous.yaml":          previous,
	} {
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		var plan operatorsv1alpha1.InstallPlan
		if err := yaml.Unmarshal(data, &plan); err != nil {
			t.Fatal(err)
		}
		if plan.Name != want.Name || plan.Namespace != want.Namespace || !reflect.DeepEqual(plan.Spec, want.Spec) {
			t.Fatalf("backup %s contains %s/%s with spec %+v, want %s/%s with spec %+v", path, plan.Namespace, plan.Name, plan.Spec, want.Namespace, want.Name, want.Spec)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "installplans/current.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ambiguous same-name backup at installplans/current.yaml: %v", err)
	}
}

func TestBackupDirectoryWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		name, obstruction, want string
		directory               bool
	}{
		{name: "directory creation", obstruction: "blocked", want: "create backup directory"},
		{name: "subscription", obstruction: "subscription.yaml", want: "write subscription.yaml", directory: true},
		{name: "operatorgroup", obstruction: "operatorgroup.yaml", want: "write operatorgroup.yaml", directory: true},
		{name: "csv", obstruction: "clusterserviceversion.yaml", want: "write clusterserviceversion.yaml", directory: true},
		{name: "plan directory", obstruction: "installplans", want: "create installplans directory"},
		{name: "plan file", obstruction: "installplans/current.yaml", want: "write installplan", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tc.obstruction)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if tc.directory {
				if err := os.Mkdir(path, 0o750); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := root
			if tc.name == "directory creation" {
				dir = filepath.Join(path, "nested")
			}
			err := diskBackupFixture().SaveToDisk(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SaveToDisk() error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestBackupOptionalResources(t *testing.T) {
	b := diskBackupFixture()
	b.OperatorGroup, b.InstallPlan = nil, nil
	dir := t.TempDir()
	if err := b.SaveToDisk(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"operatorgroup.yaml", "installplans"} {
		if _, err := os.Stat(filepath.Join(dir, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("optional backup %s: %v", path, err)
		}
	}
}

func TestBackupManifestMarshalFailure(t *testing.T) {
	err := writeYAMLFile(filepath.Join(t.TempDir(), "invalid.yaml"), map[string]interface{}{"unsupported": func() {}})
	if err == nil {
		t.Fatal("manifest serialization unexpectedly accepted an unsupported value")
	}
}

func TestBackupCollectsOnlyAssociatedInstallPlans(t *testing.T) {
	b := diskBackupFixture()
	previous := b.InstallPlan.DeepCopy()
	previous.Name = "previous"
	resolved := &operatorsv1alpha1.InstallPlan{ObjectMeta: metav1.ObjectMeta{Name: "resolved", Namespace: "operators"}, Status: operatorsv1alpha1.InstallPlanStatus{Plan: []*operatorsv1alpha1.Step{{Resolving: b.ClusterServiceVersion.Name}}}}
	unrelated := b.InstallPlan.DeepCopy()
	unrelated.Name = "unrelated"
	unrelated.Spec.ClusterServiceVersionNames = []string{"other.v1"}
	otherNamespace := b.InstallPlan.DeepCopy()
	otherNamespace.Namespace = "other"
	m := migrationTestClient(t, b.Subscription, b.OperatorGroup, b.InstallPlan, previous, resolved, unrelated, otherNamespace)
	backup, err := m.BackupResources(t.Context(), Options{SubscriptionName: b.Subscription.Name, SubscriptionNamespace: b.Subscription.Namespace, BackupDirectory: "requested"}, b.ClusterServiceVersion, b.InstallPlan)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(backup.InstallPlans))
	for _, plan := range backup.InstallPlans {
		names = append(names, plan.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"current", "previous", "resolved"}) {
		t.Fatalf("associated plans = %v", names)
	}
	b.InstallPlan.Spec.ClusterServiceVersionNames[0] = "changed"
	if backup.InstallPlan.Spec.ClusterServiceVersionNames[0] != "widgets.v1" {
		t.Fatal("backup aliases the profiled plan")
	}
}

type backupPlanListFailure struct{ client.Client }

func (c backupPlanListFailure) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*operatorsv1alpha1.InstallPlanList); ok {
		return errors.New("plan listing forbidden")
	}
	return c.Client.List(ctx, list, opts...)
}

func TestBackupPlanListFailureIsInformational(t *testing.T) {
	b := diskBackupFixture()
	m := migrationTestClient(t, b.Subscription, b.OperatorGroup)
	m.Client = backupPlanListFailure{Client: m.Client}
	var events []ProgressEvent
	m.Progress = func(event ProgressEvent) { events = append(events, event) }
	opts := Options{SubscriptionName: b.Subscription.Name, SubscriptionNamespace: b.Subscription.Namespace, BackupDirectory: "requested"}
	backup, err := m.BackupResources(t.Context(), opts, b.ClusterServiceVersion, b.InstallPlan)
	if err != nil || backup.InstallPlan.Name != "current" {
		t.Fatalf("current plan fallback = %#v, %v", backup, err)
	}
	if len(events) != 1 || events[0].Step != ProgressStepBackup || events[0].Status != ProgressWarning || events[0].Err == nil || !strings.Contains(events[0].Err.Error(), "plan listing forbidden") {
		t.Fatalf("incomplete informational backup did not emit a typed warning: %#v", events)
	}
	events = nil
	opts.BackupDirectory = ""
	if _, err := m.BackupResources(t.Context(), opts, b.ClusterServiceVersion, nil); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatal("migration without disk backup unnecessarily listed plans")
	}
}
