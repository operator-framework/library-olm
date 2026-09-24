package main

import (
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

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

	plan := strings.Join(dryRunCleanupPlan(opts, info), "\n")
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
	plan = strings.Join(dryRunCleanupPlan(opts, info), "\n")
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
	plan := strings.Join(dryRunCleanupPlan(opts, info), "\n")
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
	plan = strings.Join(dryRunCleanupPlan(opts, info), "\n")
	if !strings.Contains(plan, "Delete source namespace operators after migration (--acknowledge-namespace-delete).") {
		t.Fatalf("dry-run cleanup plan does not disclose source deletion:\n%s", plan)
	}
}
