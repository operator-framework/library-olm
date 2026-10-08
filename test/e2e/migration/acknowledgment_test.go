//go:build e2e

package e2e

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

type acknowledgmentCase struct {
	flag, check string
	mutate      func(*testing.T, context.Context, client.Client, *operatorsv1alpha1.Subscription, *operatorsv1alpha1.ClusterServiceVersion, *operatorsv1.OperatorGroup)
}

func acknowledgmentCases() map[string]acknowledgmentCase {
	return map[string]acknowledgmentCase{
		"watch-target": {flag: "watch-scope-change", check: "AllNamespaces mode", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, _ *operatorsv1alpha1.ClusterServiceVersion, og *operatorsv1.OperatorGroup) {
			og.Spec.TargetNamespaces = []string{og.Namespace}
			if err := c.Update(ctx, og); err != nil {
				t.Fatal(err)
			}
		}},
		"watch-selector": {flag: "watch-scope-change", check: "No namespace selector", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, _ *operatorsv1alpha1.ClusterServiceVersion, og *operatorsv1.OperatorGroup) {
			og.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"migration-test": "scoped"}}
			if err := c.Update(ctx, og); err != nil {
				t.Fatal(err)
			}
		}},
		"unsupported-all-namespaces": {check: "AllNamespaces install mode supported", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, csv *operatorsv1alpha1.ClusterServiceVersion, og *operatorsv1.OperatorGroup) {
			og.Spec.TargetNamespaces = []string{og.Namespace}
			if err := c.Update(ctx, og); err != nil {
				t.Fatal(err)
			}
			csv.Spec.InstallModes = []operatorsv1alpha1.InstallMode{{Type: operatorsv1alpha1.InstallModeTypeOwnNamespace, Supported: true}}
			if err := c.Update(ctx, csv); err != nil {
				t.Fatal(err)
			}
		}},
		"operator-condition": {flag: "operator-condition", check: "No OperatorCondition usage", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, csv *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
			condition := &operatorsv1.OperatorCondition{ObjectMeta: metav1.ObjectMeta{Name: csv.Name, Namespace: csv.Namespace}}
			if err := c.Create(ctx, condition); err != nil {
				t.Fatal(err)
			}
			condition.Status.Conditions = []metav1.Condition{{Type: "Upgradeable", Status: metav1.ConditionFalse, Reason: "TestBlocked", Message: "acknowledgment test", LastTransitionTime: metav1.Now()}}
			if err := c.Status().Update(ctx, condition); err != nil {
				t.Fatal(err)
			}
		}},
		"olmv0-api-access": {flag: "olmv0-api-access", check: "OLMv0-API RBAC", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, csv *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
			csv.Spec.InstallStrategy.StrategySpec.ClusterPermissions = append(csv.Spec.InstallStrategy.StrategySpec.ClusterPermissions, operatorsv1alpha1.StrategyDeploymentPermissions{
				ServiceAccountName: "ecr-secret-operator-controller-manager", Rules: []rbacv1.PolicyRule{{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"subscriptions"}, Verbs: []string{"get"}}},
			})
			if err := c.Update(ctx, csv); err != nil {
				t.Fatal(err)
			}
		}},
		"scoped-serviceaccount": {flag: "scoped-serviceaccount", check: "No scoped ServiceAccount", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, _ *operatorsv1alpha1.ClusterServiceVersion, og *operatorsv1.OperatorGroup) {
			og.Spec.ServiceAccountName = "ecr-secret-operator-controller-manager"
			if err := c.Update(ctx, og); err != nil {
				t.Fatal(err)
			}
		}},
		"subscription-state": {flag: "not-steady-state", check: "Subscription state", mutate: func(t *testing.T, ctx context.Context, c client.Client, sub *operatorsv1alpha1.Subscription, _ *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
			sub.Status.State = operatorsv1alpha1.SubscriptionStateUpgradeAvailable
			if err := c.Status().Update(ctx, sub); err != nil {
				t.Fatal(err)
			}
		}},
		"csv-phase": {flag: "not-steady-state", check: "CSV health", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, csv *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
			csv.Status.Phase = operatorsv1alpha1.CSVPhasePending
			if err := c.Status().Update(ctx, csv); err != nil {
				t.Fatal(err)
			}
		}},
		"package-dependency": {check: "No dependency resolution", mutate: dependencyProperty("olm.package.required", `{"packageName":"another-package","versionRange":">=1.0.0"}`)},
		"gvk-dependency":     {check: "No dependency resolution", mutate: dependencyProperty("olm.gvk.required", `{"group":"another.example.com","version":"v1","kind":"Another"}`)},
		"apiservice": {check: "No APIService definitions", mutate: func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, csv *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
			csv.Spec.APIServiceDefinitions.Owned = []operatorsv1alpha1.APIServiceDescription{{Name: "v1.migration.example.com", Group: "migration.example.com", Version: "v1", Kind: "Migration", DeploymentName: "ecr-secret-operator-controller-manager", ContainerPort: 443}}
			if err := c.Update(ctx, csv); err != nil {
				t.Fatal(err)
			}
		}},
		"generated-dependency": {check: "Not a dependency", mutate: func(t *testing.T, ctx context.Context, c client.Client, sub *operatorsv1alpha1.Subscription, _ *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
			if sub.Annotations == nil {
				sub.Annotations = map[string]string{}
			}
			sub.Annotations["olm.generated-by"] = "parent-installplan"
			if err := c.Update(ctx, sub); err != nil {
				t.Fatal(err)
			}
		}},
		"missing-package": {check: "No ClusterCatalog found", mutate: func(t *testing.T, ctx context.Context, c client.Client, sub *operatorsv1alpha1.Subscription, _ *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
			sub.Spec.Package = "migration-acknowledgment-package-does-not-exist"
			if err := c.Update(ctx, sub); err != nil {
				t.Fatal(err)
			}
		}},
	}
}

func dependencyProperty(kind, value string) func(*testing.T, context.Context, client.Client, *operatorsv1alpha1.Subscription, *operatorsv1alpha1.ClusterServiceVersion, *operatorsv1.OperatorGroup) {
	return func(t *testing.T, ctx context.Context, c client.Client, _ *operatorsv1alpha1.Subscription, csv *operatorsv1alpha1.ClusterServiceVersion, _ *operatorsv1.OperatorGroup) {
		if csv.Annotations == nil {
			csv.Annotations = map[string]string{}
		}
		csv.Annotations["operatorframework.io/properties"] = `[{"type":"` + kind + `","value":` + value + `}]`
		if err := c.Update(ctx, csv); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFixtureAcknowledgments(t *testing.T) {
	caseName := os.Getenv("E2E_ACKNOWLEDGMENT_CASE")
	if caseName == "" {
		t.Skip("dedicated acknowledgment fixture invocation")
	}
	if os.Getenv("E2E_SUITE") != "fixture" {
		t.Fatal("acknowledgment tests require controller-free OLMv0 fixtures")
	}
	tc, ok := acknowledgmentCases()[caseName]
	if !ok {
		t.Fatalf("unknown acknowledgment case %q", caseName)
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
		t.Fatalf("expected one fixture OperatorGroup, got %d", len(groups.Items))
	}
	og := &groups.Items[0]
	tc.mutate(t, ctx, c, &sub, &csv, og)
	assertUnchanged := func() {
		t.Helper()
		var afterSub operatorsv1alpha1.Subscription
		var afterCSV operatorsv1alpha1.ClusterServiceVersion
		var afterOG operatorsv1.OperatorGroup
		for _, obj := range []client.Object{&afterSub, &afterCSV, &afterOG} {
			key := client.ObjectKey{Namespace: namespace, Name: name}
			switch obj.(type) {
			case *operatorsv1alpha1.ClusterServiceVersion:
				key.Name = csv.Name
			case *operatorsv1.OperatorGroup:
				key.Name = og.Name
			}
			if err := c.Get(ctx, key, obj); err != nil {
				t.Fatal(err)
			}
		}
		if afterSub.UID != sub.UID || !reflect.DeepEqual(afterSub.Spec, sub.Spec) || !reflect.DeepEqual(afterSub.Status, sub.Status) || !reflect.DeepEqual(afterSub.Annotations, sub.Annotations) ||
			afterCSV.UID != csv.UID || !reflect.DeepEqual(afterCSV.Spec, csv.Spec) || !reflect.DeepEqual(afterCSV.Status, csv.Status) || !reflect.DeepEqual(afterCSV.Annotations, csv.Annotations) ||
			afterOG.UID != og.UID || !reflect.DeepEqual(afterOG.Spec, og.Spec) {
			t.Fatal("refusal or dry-run changed source resources")
		}
		assertNoMigrationObjects(t, name)
	}
	cli := binary(t, "migrate-operators-v0-to-v1")
	args := []string{"convert", name, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG")}
	expectCheckFailure(t, tc.check, cli, "check", name, "-n", namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	expectFailure(t, cli, args...)
	assertUnchanged()
	allFlags := []string{"--acknowledge-watch-scope-change", "--acknowledge-operator-condition", "--acknowledge-olmv0-api-access", "--acknowledge-scoped-serviceaccount", "--acknowledge-not-steady-state"}
	if tc.flag == "" {
		for _, preview := range []bool{false, true} {
			attempt := append(append([]string{}, args...), allFlags...)
			if preview {
				attempt = append(attempt, "--dry-run")
			}
			expectFailure(t, cli, attempt...)
			assertUnchanged()
		}
		return
	}
	wrongFlag := "--acknowledge-not-steady-state"
	if tc.flag == "not-steady-state" {
		wrongFlag = "--acknowledge-watch-scope-change"
	}
	expectFailure(t, cli, append(append([]string{}, args...), wrongFlag)...)
	assertUnchanged()
	ackArgs := append(append([]string{}, args...), "--acknowledge-"+tc.flag)
	run(t, cli, append(append([]string{}, ackArgs...), "--dry-run")...)
	assertUnchanged()
	run(t, cli, ackArgs...)
	run(t, "kubectl", "wait", "--for=condition=Installed", "clusterextension/"+name, "--timeout=10m")
	var ce ocv1.ClusterExtension
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &ce); err != nil {
		t.Fatal(err)
	}
	if ce.Annotations[migration.AnnotationAcknowledgedPrefix+tc.flag] != "true" {
		t.Fatal("missing acknowledgment audit annotation")
	}
	for key := range ce.Annotations {
		if strings.HasPrefix(key, migration.AnnotationAcknowledgedPrefix) && key != migration.AnnotationAcknowledgedPrefix+tc.flag {
			t.Fatalf("unrequested acknowledgment recorded: %s", key)
		}
	}
}
