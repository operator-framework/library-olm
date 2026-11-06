package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDryRunPreviewSortsResourceKinds(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = reader.Close()
		_ = writer.Close()
	})

	kinds := []string{"Secret", "ConfigMap", "Service", "Deployment", "Role"}
	objects := make([]unstructured.Unstructured, 0, len(kinds))
	for _, kind := range kinds {
		obj := unstructured.Unstructured{}
		obj.SetKind(kind)
		objects = append(objects, obj)
	}
	opts := migration.Options{SubscriptionName: "widget", SubscriptionNamespace: "operators", ClusterExtensionName: "widget"}
	info := &migration.MigrationInfo{PackageName: "widget", Version: "1.0.0", CollectedObjects: objects}
	for range 5 {
		if err := printDryRunPreview(opts, opts, info); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = originalStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.Split(string(output), "Resources that would be placed into ClusterObjectSet widget-1:")
	if len(sections) != 6 {
		t.Fatalf("got %d preview sections, want 5", len(sections)-1)
	}
	for i, section := range sections[1:] {
		section = strings.SplitN(section, "ClusterExtension that would be created:", 2)[0]
		previous := -1
		for _, kind := range []string{"ConfigMap", "Deployment", "Role", "Secret", "Service"} {
			position := strings.Index(section, kind+":")
			if position < 0 || position <= previous {
				t.Fatalf("preview %d has unsorted resource kinds: %s", i+1, section)
			}
			previous = position
		}
	}
}

func TestDryRunCleanupPlan(t *testing.T) {
	opts := migration.Options{
		SubscriptionName:      "widget-operator",
		SubscriptionNamespace: "operators",
		DeleteOperatorGroup:   true,
	}
	info := &migration.MigrationInfo{
		PackageName: "widgets",
		BundleName:  "widgets.v1.2.3",
	}

	plan := strings.Join(dryRunCleanupPlan(opts, opts, info), "\n")
	for _, expected := range []string{
		"Delete Subscription operators/widget-operator with orphan propagation",
		"Delete ClusterServiceVersion operators/widgets.v1.2.3 with orphan propagation",
		"Delete Operator CR widgets.operators",
		"Delete OperatorCondition operators/widgets.v1.2.3 if present",
		"Delete copied ClusterServiceVersions derived from widgets.v1.2.3 if present",
		"Retain InstallPlan resources",
		"Delete OperatorGroup(s) only when no Subscriptions remain",
	} {
		if !strings.Contains(plan, expected) {
			t.Errorf("dry-run cleanup plan does not include %q:\n%s", expected, plan)
		}
	}

	opts.DeleteOperatorGroup = false
	plan = strings.Join(dryRunCleanupPlan(opts, opts, info), "\n")
	if !strings.Contains(plan, "Retain OperatorGroup(s); --delete-operatorgroup was not specified.") {
		t.Fatalf("dry-run cleanup plan does not describe the default OperatorGroup behavior:\n%s", plan)
	}
}

func TestDryRunCleanupPlanNamespaceChange(t *testing.T) {
	opts := migration.Options{
		SubscriptionName:      "widget-operator",
		SubscriptionNamespace: "operators",
		InstallNamespace:      "widget-system",
	}
	info := &migration.MigrationInfo{PackageName: "widgets", BundleName: "widgets.v1.2.3"}
	plan := strings.Join(dryRunCleanupPlan(opts, opts, info), "\n")
	for _, expected := range []string{
		"Create or update install namespace widget-system with PSA/SCC labels copied from operators.",
		"Move collected namespaced operator resources from operators to widget-system and delete their source copies after ClusterExtension installation.",
		"Retain source namespace operators; --acknowledge-namespace-delete was not specified.",
	} {
		if !strings.Contains(plan, expected) {
			t.Fatalf("dry-run cleanup plan missing %q:\n%s", expected, plan)
		}
	}

	opts.AcknowledgeNamespaceDelete = true
	plan = strings.Join(dryRunCleanupPlan(opts, opts, info), "\n")
	if !strings.Contains(plan, "Delete source namespace operators after migration (--acknowledge-namespace-delete); this also removes any remaining InstallPlans and OperatorGroups.") {
		t.Fatalf("dry-run cleanup plan does not disclose source deletion:\n%s", plan)
	}
	if strings.Contains(plan, "Retain InstallPlan resources") || strings.Contains(plan, "Retain OperatorGroup(s)") {
		t.Fatalf("dry-run cleanup plan incorrectly retains resources removed with the source namespace:\n%s", plan)
	}
}

func TestDryRunCleanupPlanSystemManagedNamespace(t *testing.T) {
	opts := migration.Options{
		SubscriptionName:              "widget-operator",
		SubscriptionNamespace:         "operators",
		SystemManagedInstallNamespace: true,
	}
	resourceOpts := opts
	resourceOpts.InstallNamespace = "widgets-system"
	info := &migration.MigrationInfo{PackageName: "widgets", BundleName: "widgets.v1.2.3"}
	plan := strings.Join(dryRunCleanupPlan(opts, resourceOpts, info), "\n")
	for _, expected := range []string{
		"Prepare install namespace widgets-system from bundle metadata for the migration COS; ClusterExtension.spec.namespace is omitted and OLMv1 manages the namespace.",
		"Move collected namespaced operator resources from operators to widgets-system and delete their source copies after ClusterExtension installation.",
		"Retain source namespace operators; --acknowledge-namespace-delete was not specified.",
	} {
		if !strings.Contains(plan, expected) {
			t.Fatalf("dry-run system-managed cleanup plan missing %q:\n%s", expected, plan)
		}
	}
}
