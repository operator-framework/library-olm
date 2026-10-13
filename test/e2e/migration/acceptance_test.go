//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// TestFixtureAcceptanceCases runs one independently replayed OLMv0 snapshot per
// case. The dedicated target keeps these mutations out of the migration matrix.
func TestFixtureAcceptanceCases(t *testing.T) {
	caseName := os.Getenv("E2E_ACCEPTANCE_CASE")
	if caseName == "" {
		t.Skip("dedicated fixture acceptance invocation")
	}
	if os.Getenv("E2E_SUITE") != "fixture" {
		t.Fatal("acceptance cases require controller-free OLMv0 fixtures")
	}
	namespace, name := os.Getenv("E2E_NAMESPACE"), os.Getenv("E2E_SUBSCRIPTION")
	if namespace == "" || name == "" {
		t.Fatal("E2E_NAMESPACE and E2E_SUBSCRIPTION are required")
	}
	if caseName == "command-name" {
		t.Cleanup(func() {
			_, _ = output("kubectl", "delete", "clusterextension/check", "--ignore-not-found", "--wait=true")
			_, _ = output("kubectl", "delete", "clusterobjectsets", "-l", "olm.operatorframework.io/owner-name=check", "--ignore-not-found", "--wait=true")
			_, _ = output("kubectl", "delete", "secrets", "-n", "olmv1-system", "-l", "olm.operatorframework.io/owner-name=check", "--ignore-not-found", "--wait=true")
		})
	}
	t.Cleanup(func() { collectArtifacts(t, namespace) })
	_, c, _ := newMigrator(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	run(t, binary(t, "migrate-catalogs-v0-to-v1"), "--kubeconfig", os.Getenv("KUBECONFIG"))
	var sub operatorsv1alpha1.Subscription
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &sub); err != nil {
		t.Fatal(err)
	}
	cli := binary(t, "migrate-operators-v0-to-v1")
	switch caseName {
	case "non-image-source":
		testNonImageSourceRefusal(t, ctx, c, cli, &sub)
	case "dependents-warning":
		testDependentsWarning(t, ctx, c, cli, &sub)
	case "command-name":
		testCommandNamedOperator(t, ctx, c, cli, &sub)
	case "deployment-config":
		testDeploymentConfigMapping(t, ctx, c, cli, &sub)
	default:
		t.Fatalf("unknown acceptance case %q", caseName)
	}
}

func testNonImageSourceRefusal(t *testing.T, ctx context.Context, c client.Client, cli string, sub *operatorsv1alpha1.Subscription) {
	t.Helper()
	source := &operatorsv1alpha1.CatalogSource{
		ObjectMeta: metav1.ObjectMeta{Name: "fixture-configmap-source", Namespace: sub.Namespace},
		Spec:       operatorsv1alpha1.CatalogSourceSpec{SourceType: operatorsv1alpha1.SourceTypeConfigmap, ConfigMap: "fixture-catalog"},
	}
	if err := c.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	before := sub.DeepCopy()
	sub.Spec.CatalogSource, sub.Spec.CatalogSourceNamespace = source.Name, source.Namespace
	if err := c.Update(ctx, sub); err != nil {
		t.Fatal(err)
	}
	var changed operatorsv1alpha1.Subscription
	if err := c.Get(ctx, client.ObjectKeyFromObject(sub), &changed); err != nil {
		t.Fatal(err)
	}
	args := []string{sub.Name, "-n", sub.Namespace, "--kubeconfig", os.Getenv("KUBECONFIG")}
	expectCheckFailure(t, "non-image CatalogSource", cli, append([]string{"check"}, args...)...)
	for _, command := range [][]string{{"convert", "--dry-run"}, {"convert"}} {
		out, err := output(cli, append(command, args...)...)
		if err == nil || !strings.Contains(out, "non-image CatalogSource") {
			t.Fatalf("%v did not refuse source %s/%s: %v\n%s", command, source.Namespace, source.Name, err, out)
		}
	}
	var after operatorsv1alpha1.Subscription
	if err := c.Get(ctx, client.ObjectKeyFromObject(sub), &after); err != nil {
		t.Fatalf("refusal deleted Subscription: %v", err)
	}
	if after.UID != before.UID || !reflect.DeepEqual(after.Spec, changed.Spec) || !reflect.DeepEqual(after.Status, changed.Status) {
		t.Fatal("refusal changed the source Subscription")
	}
	assertNoMigrationObjects(t, sub.Name)
}

func testDependentsWarning(t *testing.T, ctx context.Context, c client.Client, cli string, sub *operatorsv1alpha1.Subscription) {
	t.Helper()
	var sourceCSV operatorsv1alpha1.ClusterServiceVersion
	if err := c.Get(ctx, client.ObjectKey{Namespace: sub.Namespace, Name: sub.Status.InstalledCSV}, &sourceCSV); err != nil {
		t.Fatal(err)
	}
	dependentCSV := sourceCSV.DeepCopy()
	dependentCSV.ObjectMeta = metav1.ObjectMeta{
		Name: "fixture-dependent.v1", Namespace: sub.Namespace,
		Annotations: map[string]string{"operatorframework.io/properties": `[{"type":"olm.package.required","value":{"packageName":"` + sub.Spec.Package + `"}}]`},
	}
	if err := c.Create(ctx, dependentCSV); err != nil {
		t.Fatal(err)
	}
	dependent := &operatorsv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{Name: "fixture-dependent", Namespace: sub.Namespace},
		Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "fixture-dependent", CatalogSource: sub.Spec.CatalogSource,
			CatalogSourceNamespace: sub.Spec.CatalogSourceNamespace},
	}
	if err := c.Create(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	dependent.Status.InstalledCSV = dependentCSV.Name
	dependent.Status.LastUpdated = metav1.Now()
	if err := c.Status().Update(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	args := []string{sub.Name, "-n", sub.Namespace, "--kubeconfig", os.Getenv("KUBECONFIG")}
	for _, command := range [][]string{{"check"}, {"convert", "--dry-run"}} {
		out, err := output(cli, append(command, args...)...)
		if err != nil || !strings.Contains(out, sub.Namespace+"/"+dependent.Name) || !strings.Contains(out, "may depend") {
			t.Fatalf("%v omitted dependent warning: %v\n%s", command, err, out)
		}
	}
	for _, command := range [][]string{{"check", "--all"}, {"convert", "--dry-run", "--output=jsonl", sub.Name, "-n", sub.Namespace}} {
		out, err := output(cli, append(command, "--kubeconfig", os.Getenv("KUBECONFIG"))...)
		if err != nil || !strings.Contains(out, sub.Namespace+"/"+dependent.Name) || !strings.Contains(out, "may depend") {
			t.Fatalf("%v omitted dependent warning: %v\n%s", command, err, out)
		}
	}
	out, err := output(cli, append([]string{"convert"}, args...)...)
	if err != nil || !strings.Contains(out, sub.Namespace+"/"+dependent.Name) || !strings.Contains(out, "may depend") {
		t.Fatalf("conversion omitted dependent warning: %v\n%s", err, out)
	}
	run(t, "kubectl", "wait", "--for=condition=Installed", "clusterextension/"+sub.Name, "--timeout=10m")
	if err := c.Get(ctx, client.ObjectKeyFromObject(dependent), &operatorsv1alpha1.Subscription{}); err != nil {
		t.Fatalf("migration removed dependent Subscription: %v", err)
	}
}

func testCommandNamedOperator(t *testing.T, ctx context.Context, c client.Client, cli string, sub *operatorsv1alpha1.Subscription) {
	t.Helper()
	// Keep the installed package and CSV, but make the Subscription name collide
	// with a CLI verb. No OLMv0 controller runs in this fixture cluster.
	alias := sub.DeepCopy()
	alias.ObjectMeta = metav1.ObjectMeta{Name: "check", Namespace: sub.Namespace}
	alias.Status = operatorsv1alpha1.SubscriptionStatus{}
	if err := c.Create(ctx, alias); err != nil {
		t.Fatal(err)
	}
	alias.Status = *sub.Status.DeepCopy()
	if err := c.Status().Update(ctx, alias); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, sub); err != nil {
		t.Fatal(err)
	}
	args := []string{"convert", alias.Name, "-n", alias.Namespace, "--kubeconfig", os.Getenv("KUBECONFIG")}
	run(t, cli, append(args, "--dry-run")...)
	run(t, cli, args...)
	run(t, "kubectl", "wait", "--for=condition=Installed", "clusterextension/"+alias.Name, "--timeout=10m")
	if err := c.Get(ctx, client.ObjectKeyFromObject(alias), &operatorsv1alpha1.Subscription{}); err == nil {
		t.Fatal("command-named Subscription remains after conversion")
	}
	var ce ocv1.ClusterExtension
	if err := c.Get(ctx, client.ObjectKey{Name: alias.Name}, &ce); err != nil || ce.Spec.Source.Catalog.PackageName != sub.Spec.Package {
		t.Fatalf("command-named conversion installed wrong package: %v, %#v", err, ce.Spec.Source.Catalog)
	}
}

func testDeploymentConfigMapping(t *testing.T, ctx context.Context, c client.Client, cli string, sub *operatorsv1alpha1.Subscription) {
	t.Helper()
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "fixture-config", Namespace: sub.Namespace}, Data: map[string]string{"MODE": "test"}}
	if err := c.Create(ctx, configMap); err != nil {
		t.Fatal(err)
	}
	config := &operatorsv1alpha1.SubscriptionConfig{
		NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
		Tolerations:  []corev1.Toleration{{Key: "fixture", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
		Resources:    &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
		EnvFrom:      []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name}}}},
		Env:          []corev1.EnvVar{{Name: "MIGRATION_TEST", Value: "mapped"}},
		Volumes:      []corev1.Volume{{Name: "fixture", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name}}}}},
		VolumeMounts: []corev1.VolumeMount{{Name: "fixture", MountPath: "/etc/fixture", ReadOnly: true}},
		Affinity:     &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1, Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "kubernetes.io/os", Operator: corev1.NodeSelectorOpIn, Values: []string{"linux"}}}}}}}},
		Annotations:  map[string]string{"migration.example.com/configured": "true"},
		Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ignored"}},
	}
	sub.Spec.Config = config
	if err := c.Update(ctx, sub); err != nil {
		t.Fatal(err)
	}
	out, err := output(cli, "convert", sub.Name, "-n", sub.Namespace, "--kubeconfig", os.Getenv("KUBECONFIG"))
	if err != nil || !strings.Contains(out, "selector") {
		t.Fatalf("conversion omitted selector-drop warning: %v\n%s", err, out)
	}
	run(t, "kubectl", "wait", "--for=condition=Installed", "clusterextension/"+sub.Name, "--timeout=10m")
	var ce ocv1.ClusterExtension
	if err := c.Get(ctx, client.ObjectKey{Name: sub.Name}, &ce); err != nil {
		t.Fatal(err)
	}
	if ce.Spec.Config == nil || ce.Spec.Config.Inline == nil {
		t.Fatal("ClusterExtension is missing inline deployment config")
	}
	var inline struct {
		DeploymentConfig operatorsv1alpha1.SubscriptionConfig `json:"deploymentConfig"`
	}
	if err := json.Unmarshal(ce.Spec.Config.Inline.Raw, &inline); err != nil {
		t.Fatal(err)
	}
	want := config.DeepCopy()
	want.Selector = nil
	if !reflect.DeepEqual(&inline.DeploymentConfig, want) {
		t.Fatalf("ClusterExtension deployment config = %#v, want %#v", inline.DeploymentConfig, *want)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(sub), &operatorsv1alpha1.Subscription{}); err == nil {
		t.Fatal("OLMv0 Subscription remains after configured conversion")
	}
	var deployments appsv1.DeploymentList
	if err := c.List(ctx, &deployments, client.InNamespace(sub.Namespace)); err != nil || len(deployments.Items) == 0 {
		t.Fatalf("list configured operator Deployments: %v, count=%d", err, len(deployments.Items))
	}
	configured := false
	for _, deployment := range deployments.Items {
		pod := deployment.Spec.Template
		if !reflect.DeepEqual(pod.Spec.NodeSelector, config.NodeSelector) ||
			!reflect.DeepEqual(pod.Spec.Tolerations, config.Tolerations) ||
			!reflect.DeepEqual(pod.Spec.Affinity, config.Affinity) ||
			pod.Annotations["migration.example.com/configured"] != "true" {
			t.Fatalf("operator Deployment lost pod-level deploymentConfig fields: %#v", pod)
		}
		volumeFound := false
		for _, volume := range pod.Spec.Volumes {
			if volume.Name == "fixture" && volume.ConfigMap != nil && volume.ConfigMap.Name == configMap.Name {
				volumeFound = true
			}
		}
		if !volumeFound {
			t.Fatal("operator Deployment is missing configured volume")
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "manager" {
				continue
			}
			if container.Resources.Requests.Cpu().Cmp(resource.MustParse("100m")) != 0 ||
				!reflect.DeepEqual(container.EnvFrom, config.EnvFrom) {
				t.Fatalf("operator container lost configured resources or envFrom: %#v", container)
			}
			mountFound := false
			for _, mount := range container.VolumeMounts {
				if mount.Name == "fixture" && mount.MountPath == "/etc/fixture" && mount.ReadOnly {
					mountFound = true
				}
			}
			if !mountFound {
				t.Fatal("operator container is missing configured volumeMount")
			}
			for _, env := range container.Env {
				if env.Name == "MIGRATION_TEST" && env.Value == "mapped" {
					configured = true
				}
			}
		}
	}
	if !configured {
		t.Fatal("OLMv1 operator Deployment did not receive mapped environment")
	}
}
