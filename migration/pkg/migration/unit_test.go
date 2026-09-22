package migration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
	ocv1ac "github.com/operator-framework/operator-controller/applyconfigurations/api/v1"
)

type failingMigrationClient struct {
	client.Client
	failCOSCreate    bool
	unknownCOSCreate bool
	failCECreate     bool
	blockCOS         bool
	succeedCOS       bool
}

func (c failingMigrationClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if c.failCOSCreate {
		if _, ok := obj.(*ocv1.ClusterObjectSet); ok {
			return apierrors.NewAlreadyExists(ocv1.GroupVersion.WithResource("clusterobjectsets").GroupResource(), obj.GetName())
		}
	}
	if c.unknownCOSCreate {
		if _, ok := obj.(*ocv1.ClusterObjectSet); ok {
			return errors.New("simulated ClusterObjectSet create transport failure")
		}
	}
	if c.failCECreate {
		if _, ok := obj.(*ocv1.ClusterExtension); ok {
			return apierrors.NewAlreadyExists(ocv1.GroupVersion.WithResource("clusterextensions").GroupResource(), obj.GetName())
		}
	}
	if c.succeedCOS {
		if cos, ok := obj.(*ocv1.ClusterObjectSet); ok {
			cos.Status.Conditions = []metav1.Condition{{Type: ocv1.ClusterObjectSetTypeSucceeded, Status: metav1.ConditionTrue}}
		}
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c failingMigrationClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.blockCOS {
		if cos, ok := obj.(*ocv1.ClusterObjectSet); ok {
			cos.Status.Conditions = []metav1.Condition{{Type: ocv1.ClusterObjectSetTypeSucceeded, Status: metav1.ConditionFalse, Reason: ocv1.ClusterObjectSetReasonBlocked}}
			return nil
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func migrationTestClient(t *testing.T, objects ...runtime.Object) *Migrator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := operatorsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := operatorsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := ocv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return NewMigrator(fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build(), nil)
}

func healthySubscriptionFixtures() (*operatorsv1alpha1.Subscription, *operatorsv1alpha1.ClusterServiceVersion) {
	sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "ns"}, Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "widgets"}, Status: operatorsv1alpha1.SubscriptionStatus{InstalledCSV: "widgets.v1", State: operatorsv1alpha1.SubscriptionStateAtLatest}}
	csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "ns"}, Status: operatorsv1alpha1.ClusterServiceVersionStatus{Phase: operatorsv1alpha1.CSVPhaseSucceeded, Reason: operatorsv1alpha1.CSVReasonInstallSuccessful}}
	return sub, csv
}

func establishedClusterObjectSetCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: clusterObjectSetCRDName},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
			Type:   apiextensionsv1.Established,
			Status: apiextensionsv1.ConditionTrue,
		}}},
	}
}

func TestCheckReadiness(t *testing.T) {
	sub, csv := healthySubscriptionFixtures()
	m := migrationTestClient(t, sub, csv, establishedClusterObjectSetCRD())
	report, err := m.CheckReadiness(context.Background(), Options{SubscriptionName: "sub", SubscriptionNamespace: "ns"})
	if err != nil || !report.Passed() {
		t.Fatalf("healthy report=%#v err=%v", report, err)
	}

	busySub, busyCSV := healthySubscriptionFixtures()
	busySub.Status.State = operatorsv1alpha1.SubscriptionStateUpgradeAvailable
	busyCSV.Status.Phase = operatorsv1alpha1.CSVPhaseFailed
	m = migrationTestClient(t, busySub, busyCSV, establishedClusterObjectSetCRD())
	report, err = m.CheckReadiness(context.Background(), Options{SubscriptionName: "sub", SubscriptionNamespace: "ns"})
	if err != nil || report.Passed() || len(report.FailedChecks()) != 2 {
		t.Fatalf("unacknowledged report=%#v err=%v", report, err)
	}
	report, err = m.CheckReadiness(context.Background(), Options{SubscriptionName: "sub", SubscriptionNamespace: "ns", AcknowledgeNotSteadyState: true})
	if err != nil || !report.Passed() {
		t.Fatalf("acknowledged report=%#v err=%v", report, err)
	}

	depSub, depCSV := healthySubscriptionFixtures()
	depSub.Annotations = map[string]string{"olm.generated-by": "parent"}
	m = migrationTestClient(t, depSub, depCSV, establishedClusterObjectSetCRD(), &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "other"}, Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "widgets"}})
	report, err = m.CheckReadiness(context.Background(), Options{SubscriptionName: "sub", SubscriptionNamespace: "ns"})
	if err != nil || report.Passed() || len(report.FailedChecks()) != 2 {
		t.Fatalf("dependency/duplicate report=%#v err=%v", report, err)
	}
}

func TestCheckReadinessRequiresClusterObjectSetCRD(t *testing.T) {
	sub, csv := healthySubscriptionFixtures()
	m := migrationTestClient(t, sub, csv)
	report, err := m.CheckReadiness(context.Background(), Options{SubscriptionName: "sub", SubscriptionNamespace: "ns"})
	if err != nil {
		t.Fatalf("CheckReadiness() error = %v", err)
	}
	for _, check := range report.Checks {
		if check.Name == "ClusterObjectSet API" {
			if check.Passed || !strings.Contains(check.Message, clusterObjectSetCRDName) {
				t.Fatalf("ClusterObjectSet API check = %#v, want failed missing-CRD check", check)
			}
			return
		}
	}
	t.Fatal("CheckReadiness() did not report the ClusterObjectSet API check")
}

func TestCompatibilityOperatorGroupAndConditionOverrides(t *testing.T) {
	og := &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "og", Namespace: "ns"}, Spec: operatorsv1.OperatorGroupSpec{TargetNamespaces: []string{"target"}, ServiceAccountName: "restricted"}}
	csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "ns"}}
	oc := &operatorsv1.OperatorCondition{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "ns"}, Status: operatorsv1.OperatorConditionStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}}
	m := migrationTestClient(t, og, oc)
	report, err := m.CheckCompatibility(context.Background(), Options{SubscriptionNamespace: "ns"}, csv, "")
	if err != nil || report.Passed() || len(report.FailedChecks()) != 3 {
		t.Fatalf("unacknowledged=%#v err=%v", report, err)
	}
	report, err = m.CheckCompatibility(context.Background(), Options{SubscriptionNamespace: "ns", AcknowledgeWatchScopeChange: true, AcknowledgeScopedServiceAccount: true, AcknowledgeOperatorCondition: true}, csv, "")
	if err != nil || !report.Passed() {
		t.Fatalf("acknowledged=%#v err=%v", report, err)
	}
}

func TestCatalogParsingAndVersion(t *testing.T) {
	body := strings.NewReader(`
{"schema":"olm.package","name":"widgets","defaultChannel":"stable"}
{"schema":"olm.bundle","package":"widgets","properties":[{"type":"olm.package","value":{"packageName":"widgets","version":"1.2.3"}}]}
{"schema":"olm.channel","package":"widgets","name":"stable"}
not-json
`)
	info, err := parseCatalogResponse(body, "widgets", "1.2.3", "stable")
	if err != nil || !info.Found || !info.VersionFound || !info.ChannelFound || info.DefaultChannel != "stable" {
		t.Fatalf("unexpected catalog info %#v, err=%v", info, err)
	}
	if got := extractBundleVersion(json.RawMessage(`[{"type":"olm.package","value":{"version":"2.0.0"}}]`)); got != "2.0.0" {
		t.Fatalf("version = %q", got)
	}
	csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"operatorframework.io/properties": `[{"type":"olm.package","value":{"version":"3.1.4"}}]`}}}
	if got := parseCSVVersion(csv); got != "3.1.4" {
		t.Fatalf("CSV version = %q", got)
	}
}

func TestCatalogdTLSConfigUsesServingCertificateWithoutAPIServerTransportSettings(t *testing.T) {
	ca := []byte("catalogd-ca")
	config := &rest.Config{
		TLSClientConfig: rest.TLSClientConfig{CAData: []byte("api-server-ca"), Insecure: true},
		Proxy: func(*http.Request) (*url.URL, error) {
			return &url.URL{Scheme: "https", Host: "proxy.example"}, nil
		},
	}
	clientset := k8sfake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "catalogd", Namespace: "catalogd-ns"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name:         "catalogserver-certs",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "catalogserver-cert"}},
		}}},
	}, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "catalogserver-cert", Namespace: "catalogd-ns"},
		Data:       map[string][]byte{"ca.crt": ca},
	})

	got, err := catalogdTLSConfig(context.Background(), clientset, config, "catalogd-ns", "catalogd")
	if err != nil {
		t.Fatalf("catalogdTLSConfig() error = %v", err)
	}
	if got == config || got.Insecure || got.Proxy != nil || got.CAFile != "" || !bytes.Equal(got.CAData, ca) {
		t.Fatalf("catalog transport config = %#v, want copied config with catalog CA, TLS verification, and no proxy", got)
	}
}

func TestCatalogdServiceLocation(t *testing.T) {
	catalog := &ocv1.ClusterCatalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog"}}
	catalog.Status.URLs = &ocv1.ClusterCatalogURLs{Base: "https://catalogd-service.openshift-catalogd.svc/catalogs/catalog"}
	namespace, serverName, err := catalogdServiceLocation(catalog)
	if err != nil {
		t.Fatalf("catalogdServiceLocation() error = %v", err)
	}
	if namespace != "openshift-catalogd" || serverName != "catalogd-service.openshift-catalogd.svc" {
		t.Fatalf("catalogdServiceLocation() = %q, %q, want openshift-catalogd and service DNS name", namespace, serverName)
	}

	catalog.Status.URLs.Base = "https://catalog.example.test/catalogs/catalog"
	if _, _, err := catalogdServiceLocation(catalog); err == nil {
		t.Fatal("catalogdServiceLocation() accepted a non-service hostname")
	}
}

func TestCompatibilityPureChecks(t *testing.T) {
	for _, properties := range []string{
		`[{"type":"olm.package.required","value":{"packageName":"dep"}}]`,
		`{"properties":[{"type":"olm.gvk.required","value":{"group":"x"}}]}`,
		`not json`,
	} {
		if got := checkNoDependencies(properties); len(got) == 0 || got[0].Passed {
			t.Fatalf("dependencies %q unexpectedly passed: %#v", properties, got)
		}
	}
	if got := checkNoDependencies(`[{"type":"olm.package","value":{}}]`); !got[0].Passed {
		t.Fatalf("non-dependency failed: %#v", got)
	}
	csv := &operatorsv1alpha1.ClusterServiceVersion{}
	if !checkNoAPIServices(csv).Passed {
		t.Fatal("empty APIService definitions failed")
	}
	csv.Spec.APIServiceDefinitions.Owned = []operatorsv1alpha1.APIServiceDescription{{Name: "v1.widgets"}}
	if checkNoAPIServices(csv).Passed {
		t.Fatal("owned APIService passed")
	}
	csv.Spec.InstallStrategy.StrategySpec = operatorsv1alpha1.StrategyDetailsDeployment{ClusterPermissions: []operatorsv1alpha1.StrategyDeploymentPermissions{{Rules: []rbacv1.PolicyRule{{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"subscriptions"}}}}}}
	if checkOLMv0APIAccess(Options{}, csv).Passed {
		t.Fatal("OLMv0-only RBAC passed")
	}
	if !checkOLMv0APIAccess(Options{AcknowledgeOLMv0APIAccess: true}, csv).Passed {
		t.Fatal("acknowledged RBAC failed")
	}
	csv.Spec.InstallStrategy.StrategySpec.ClusterPermissions[0].Rules = append(csv.Spec.InstallStrategy.StrategySpec.ClusterPermissions[0].Rules, rbacv1.PolicyRule{APIGroups: []string{"olm.operatorframework.io"}})
	if !checkOLMv0APIAccess(Options{}, csv).Passed {
		t.Fatal("dual RBAC failed")
	}
}

func TestPhaseSortAndResourceKeys(t *testing.T) {
	object := func(group, kind, namespace, name string) ocv1ac.ClusterObjectSetObjectApplyConfiguration {
		u := unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: "v1", Kind: kind})
		u.SetNamespace(namespace)
		u.SetName(name)
		return *ocv1ac.ClusterObjectSetObject().WithObject(u)
	}
	phases := PhaseSort([]ocv1ac.ClusterObjectSetObjectApplyConfiguration{object("apps", "Deployment", "z", "b"), object("", "Secret", "z", "a"), object("apiextensions.k8s.io", "CustomResourceDefinition", "", "widgets.example.io"), object("apps", "Deployment", "a", "a")})
	if len(phases) != 3 || *phases[0].Name != string(PhaseConfiguration) || *phases[1].Name != string(PhaseCRDs) || *phases[2].Name != string(PhaseDeploy) {
		t.Fatalf("unexpected phases %#v", phases)
	}
	if *phases[2].CollisionProtection != ocv1.CollisionProtectionNone || phases[2].Objects[0].Object.GetNamespace() != "a" {
		t.Fatalf("deployment phase not sorted with migration collision protection: %#v", phases[2])
	}
	a := unstructured.Unstructured{}
	a.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	a.SetNamespace("ns")
	a.SetName("x")
	b := a.DeepCopy()
	b.SetAPIVersion("core/v1")
	if resourceKey(a) != resourceKey(*b) {
		t.Fatal("resource key should ignore API version")
	}
}

func TestSecretPacker(t *testing.T) {
	obj := unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "one"}, "data": map[string]interface{}{"x": "y"}}}
	phases := []*ocv1ac.ClusterObjectSetPhaseApplyConfiguration{ocv1ac.ClusterObjectSetPhase().WithObjects(ocv1ac.ClusterObjectSetObject().WithObject(obj), ocv1ac.ClusterObjectSetObject().WithObject(obj))}
	result, err := (&secretPacker{RevisionName: "rev", OwnerName: "ce", SystemNamespace: "system"}).pack(phases)
	if err != nil || len(result.Secrets) != 1 || len(result.Secrets[0].Data) != 1 || len(result.Refs) != 2 {
		t.Fatalf("pack result=%#v err=%v", result, err)
	}
	if result.Secrets[0].Type != corev1.SecretType(SecretTypeObjectData) || result.Secrets[0].Immutable == nil || !*result.Secrets[0].Immutable {
		t.Fatal("secret is not immutable object-data")
	}
	compressed, err := gzipData(bytes.Repeat([]byte("a"), gzipThreshold+1))
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	_, _ = decoded.ReadFrom(r)
	_ = r.Close()
	sameHash := contentHash([]byte("same"))
	if decoded.Len() != gzipThreshold+1 || sameHash == "" || sameHash == contentHash([]byte("different")) {
		t.Fatal("gzip/hash not deterministic")
	}
}

func TestOptionsReportsAndAnnotationFiltering(t *testing.T) {
	opts := Options{SubscriptionName: "sub", SubscriptionNamespace: "ns"}
	opts.ApplyDefaults()
	if opts.ClusterExtensionName != "sub" || opts.InstallNamespace != "ns" || opts.SystemNamespace != "" {
		t.Fatalf("defaults: %#v", opts)
	}
	report := &PreMigrationReport{Checks: []CheckResult{{Passed: true}, {Name: "bad", Passed: false}}}
	if report.Passed() || len(report.FailedChecks()) != 1 {
		t.Fatal("report predicates incorrect")
	}
	filtered := filterAnnotations(map[string]string{"keep": "x", "olm.operatorframework.io/managed": "yes", "kubectl.kubernetes.io/last-applied-configuration": "x"})
	if len(filtered) != 2 || filtered["keep"] != "x" || filtered["olm.operatorframework.io/managed"] != "yes" {
		t.Fatalf("annotations not stripped: %#v", filtered)
	}
}

func TestPrepareClusterObjectSet(t *testing.T) {
	establishedCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: clusterObjectSetCRDName},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
			Type:   apiextensionsv1.Established,
			Status: apiextensionsv1.ConditionTrue,
		}}},
	}
	controller := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatorControllerDeployName,
			Namespace: "openshift-operator-controller",
			Labels:    map[string]string{"app.kubernetes.io/name": "operator-controller"},
		},
	}
	m := migrationTestClient(t, establishedCRD, controller)

	got, err := m.PrepareClusterObjectSet(context.Background(), Options{SubscriptionName: "sub", SubscriptionNamespace: "operators"})
	if err != nil {
		t.Fatalf("PrepareClusterObjectSet() error = %v", err)
	}
	if got.SystemNamespace != "openshift-operator-controller" {
		t.Fatalf("SystemNamespace = %q, want discovered operator-controller namespace", got.SystemNamespace)
	}

	got, err = m.PrepareClusterObjectSet(context.Background(), Options{SystemNamespace: "chosen"})
	if err != nil {
		t.Fatalf("PrepareClusterObjectSet() with override error = %v", err)
	}
	if got.SystemNamespace != "chosen" {
		t.Fatalf("SystemNamespace override = %q, want chosen", got.SystemNamespace)
	}
}

func TestPrepareClusterObjectSetRejectsExistingClusterExtension(t *testing.T) {
	m := migrationTestClient(t, &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "sub"}})
	_, err := m.PrepareClusterObjectSet(context.Background(), Options{SubscriptionName: "sub", SubscriptionNamespace: "operators"})
	if err == nil || !strings.Contains(err.Error(), "ClusterExtension sub already exists") {
		t.Fatalf("PrepareClusterObjectSet() error = %v, want existing ClusterExtension error", err)
	}
}

func TestPrepareClusterObjectSetRequiresEstablishedCRD(t *testing.T) {
	m := migrationTestClient(t)
	if _, err := m.PrepareClusterObjectSet(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), clusterObjectSetCRDName) {
		t.Fatalf("PrepareClusterObjectSet() error = %v, want missing CRD error", err)
	}

	m = migrationTestClient(t, &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: clusterObjectSetCRDName},
	})
	if _, err := m.PrepareClusterObjectSet(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "not established") {
		t.Fatalf("PrepareClusterObjectSet() error = %v, want unestablished CRD error", err)
	}
}

func TestEmptyLabelSelector(t *testing.T) {
	for name, selector := range map[string]*metav1.LabelSelector{
		"nil":         nil,
		"empty":       {},
		"labels":      {MatchLabels: map[string]string{"app": "operator"}},
		"expressions": {MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: metav1.LabelSelectorOpExists}}},
	} {
		got := isEmptyLabelSelector(selector)
		want := name == "nil" || name == "empty"
		if got != want {
			t.Errorf("%s: isEmptyLabelSelector() = %t, want %t", name, got, want)
		}
	}
}

func TestCatalogCreationAndErrors(t *testing.T) {
	if got := (&PackageNotFoundError{PackageName: "widgets", Version: "1.2.3", Channel: "stable", QueriedCatalogs: []string{"one"}}).Error(); !strings.Contains(got, "widgets") || !strings.Contains(got, "stable") || !strings.Contains(got, "one") {
		t.Fatalf("unexpected package-not-found error: %q", got)
	}

	serving := &ocv1.ClusterCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "existing"},
		Status:     ocv1.ClusterCatalogStatus{Conditions: []metav1.Condition{{Type: "Serving", Status: metav1.ConditionTrue}}},
	}
	m := migrationTestClient(t, serving)
	if err := m.CreateClusterCatalog(context.Background(), "existing", "example.invalid/catalog:v1"); err == nil {
		t.Fatal("creating an existing ClusterCatalog unexpectedly succeeded")
	}
}

func TestBackupSaveToDisk(t *testing.T) {
	b := &Backup{
		Subscription:          &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "ns"}},
		ClusterServiceVersion: &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "sub.v1", Namespace: "ns"}},
		OperatorGroup:         &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "og", Namespace: "ns"}},
		InstallPlan:           &operatorsv1alpha1.InstallPlan{ObjectMeta: metav1.ObjectMeta{Name: "plan", Namespace: "ns"}},
	}
	dir := t.TempDir()
	if err := b.SaveToDisk(dir); err != nil {
		t.Fatalf("SaveToDisk: %v", err)
	}
	for file, name := range map[string]string{
		"subscription.yaml":          "sub",
		"clusterserviceversion.yaml": "sub.v1",
		"operatorgroup.yaml":         "og",
		"installplans/plan.yaml":     "plan",
	} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || !strings.Contains(string(data), name) {
			t.Errorf("backup %s = %q, err=%v", file, data, err)
		}
		if !strings.Contains(string(data), "apiVersion: operators.coreos.com/") || !strings.Contains(string(data), "kind:") {
			t.Errorf("backup %s is not an applyable Kubernetes manifest: %q", file, data)
		}
	}
}

func TestClusterRoleCleanup(t *testing.T) {
	role := func(name string, labels map[string]string) *rbacv1.ClusterRole {
		return &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}
	m := migrationTestClient(t,
		role("widgets-admin", map[string]string{"olm.owner": "widgets.v1"}),
		role("widgets-unrelated", map[string]string{"olm.owner": "widgets.v1"}),
		role("olm.og.operators.admin-abc", map[string]string{"olm.owner": "widgets.v1", "olm.managed": "true", "keep": "yes"}),
	)
	if roles := m.FindCRDClusterRoles(context.Background(), "widgets.v1"); len(roles) != 1 || roles[0] != "widgets-admin" {
		t.Fatalf("FindCRDClusterRoles() = %v", roles)
	}
	if stripped := m.stripOGAggregationClusterRoles(context.Background(), "operators"); len(stripped) != 1 || stripped[0] != "olm.og.operators.admin-abc" {
		t.Fatalf("stripOGAggregationClusterRoles() = %v", stripped)
	}
	var updated rbacv1.ClusterRole
	if err := m.Client.Get(context.Background(), client.ObjectKey{Name: "olm.og.operators.admin-abc"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Labels["keep"] != "yes" || updated.Labels["olm.owner"] != "" || updated.Labels["olm.managed"] != "" {
		t.Fatalf("unexpected labels after stripping: %#v", updated.Labels)
	}
}

func TestCleanupOperatorGroup(t *testing.T) {
	ctx := context.Background()
	og := &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "og", Namespace: "ns"}}
	m := migrationTestClient(t, og)
	actions := m.cleanupOperatorGroup(ctx, Options{SubscriptionNamespace: "ns", DeleteOperatorGroup: true})
	if len(actions) != 1 || !actions[0].Succeeded {
		t.Fatalf("cleanup actions = %#v", actions)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(og), &operatorsv1.OperatorGroup{}); err == nil {
		t.Fatal("OperatorGroup was not deleted")
	}

	og = &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "ns"}}
	sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns"}}
	m = migrationTestClient(t, og, sub)
	actions = m.cleanupOperatorGroup(ctx, Options{SubscriptionNamespace: "ns", DeleteOperatorGroup: true})
	if len(actions) != 1 || !actions[0].Skipped {
		t.Fatalf("shared cleanup actions = %#v", actions)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(og), &operatorsv1.OperatorGroup{}); err != nil {
		t.Fatalf("shared OperatorGroup was deleted: %v", err)
	}
}

func TestScanStatesAndPublicHelpers(t *testing.T) {
	ctx := context.Background()
	conflictingSub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "conflict", Namespace: "ns"}, Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "widgets"}}
	conflictingCE := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "conflict-ce", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "ns/conflict"}}}
	alreadyMigratedCE := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "done-ce", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "old/gone"}}}
	m := migrationTestClient(t, conflictingSub, conflictingCE, alreadyMigratedCE)

	results, err := m.ScanAll(ctx)
	if err != nil || len(results) != 2 {
		t.Fatalf("ScanAll() = %#v, %v", results, err)
	}
	if results[0].Status != OperatorStatusConflict || results[1].Status != OperatorStatusAlreadyMigrated {
		t.Fatalf("unexpected scan states: %#v", results)
	}
	result, err := m.Check(ctx, Options{SubscriptionName: "conflict", SubscriptionNamespace: "ns"})
	if err != nil || result.Status != OperatorStatusConflict {
		t.Fatalf("Check() = %#v, %v", result, err)
	}

	eligible := EligibleFromScan([]OperatorScanResult{{Status: OperatorStatusEligible}, {Status: OperatorStatusConflict}})
	if len(eligible) != 1 {
		t.Fatalf("EligibleFromScan() = %#v", eligible)
	}
	var summary strings.Builder
	PrintScanSummary([]OperatorScanResult{
		{Status: OperatorStatusConflict, SubscriptionNamespace: "ns", SubscriptionName: "conflict", Error: fmt.Errorf("overlap")},
		{Status: OperatorStatusIneligible, SubscriptionNamespace: "ns", SubscriptionName: "bad", FailedChecks: []CheckResult{{Name: "readiness", Message: "not ready"}}},
		{Status: OperatorStatusAlreadyMigrated, State: "ClusterExtension done-ce"},
		{Status: OperatorStatusEligible, SubscriptionNamespace: "ns", SubscriptionName: "good", PackageName: "widgets"},
	}, func(format string, args ...interface{}) { fmt.Fprintf(&summary, format, args...) })
	for _, status := range []string{"Conflict", "Ineligible", "AlreadyMigrated", "Eligible"} {
		if !strings.Contains(summary.String(), status) {
			t.Errorf("summary missing %q: %s", status, summary.String())
		}
	}
}

func TestScanAllKeepsMixedUnsafeOperatorsOutOfEligibleResults(t *testing.T) {
	ctx := context.Background()
	busySub, busyCSV := healthySubscriptionFixtures()
	busySub.Name, busyCSV.Name = "busy", "busy.v1"
	busySub.Status.InstalledCSV = busyCSV.Name
	busySub.Status.State = operatorsv1alpha1.SubscriptionStateUpgradeAvailable
	conflictSub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "conflict", Namespace: "ns"}, Spec: &operatorsv1alpha1.SubscriptionSpec{Package: "conflict"}}
	conflictCE := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "conflict", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "ns/conflict"}}}
	already := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "already", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "ns/gone"}}}
	m := migrationTestClient(t, busySub, busyCSV, conflictSub, conflictCE, already)

	results, err := m.ScanAll(ctx)
	if err != nil || len(results) != 3 {
		t.Fatalf("ScanAll() = %#v, %v", results, err)
	}
	if eligible := EligibleFromScan(results); len(eligible) != 0 {
		t.Fatalf("unsafe mixed scan returned eligible operators: %#v", eligible)
	}
	got := map[OperatorStatus]int{}
	for _, result := range results {
		got[result.Status]++
	}
	for _, status := range []OperatorStatus{OperatorStatusIneligible, OperatorStatusConflict, OperatorStatusAlreadyMigrated} {
		if got[status] != 1 {
			t.Fatalf("mixed scan status counts = %#v, want one %s", got, status)
		}
	}
}

func TestPrerequisitesAndRecoveryErrors(t *testing.T) {
	ctx := context.Background()
	sub, csv := healthySubscriptionFixtures()
	m := migrationTestClient(t, sub, csv, establishedClusterObjectSetCRD())
	gotCSV, ip, readiness, compatibility, err := m.EnsurePrerequisites(ctx, Options{SubscriptionName: "sub", SubscriptionNamespace: "ns"})
	if err != nil || gotCSV.Name != csv.Name || ip != nil || !readiness.Passed() || compatibility == nil {
		t.Fatalf("EnsurePrerequisites() = csv=%#v ip=%#v readiness=%#v compatibility=%#v err=%v", gotCSV, ip, readiness, compatibility, err)
	}
	if err := m.Migrate(ctx, Options{SubscriptionName: "missing", SubscriptionNamespace: "ns"}); err == nil {
		t.Fatal("Migrate() unexpectedly succeeded for a missing Subscription")
	}
	if err := m.RecoverFromBackup(ctx, Options{}, nil); err == nil {
		t.Fatal("RecoverFromBackup(nil) unexpectedly succeeded")
	}
	if err := m.RecoverBeforeCE(ctx, Options{ClusterExtensionName: "missing"}, nil); err == nil {
		t.Fatal("RecoverBeforeCE(nil) unexpectedly succeeded")
	}
	if _, err := m.Gather(ctx, Options{SubscriptionName: "missing", SubscriptionNamespace: "ns"}); err == nil {
		t.Fatal("Gather() unexpectedly succeeded for a missing Subscription")
	}
	if err := m.Rollback(ctx, Options{ClusterExtensionName: "missing"}); err == nil {
		t.Fatal("Rollback() unexpectedly succeeded for a missing ClusterExtension")
	}
	if err := m.Cleanup(ctx, Options{ClusterExtensionName: "missing"}); err == nil {
		t.Fatal("Cleanup() unexpectedly succeeded for a missing ClusterExtension")
	}
}

// TestMigrateRejectsUnsafeOperatorsWithoutMutation verifies the safety boundary
// of conversion: failures before preparation must leave OLMv0 resources intact.
func TestMigrateRejectsUnsafeOperatorsWithoutMutation(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate func(*operatorsv1alpha1.Subscription, *operatorsv1alpha1.ClusterServiceVersion)
	}{
		{
			name: "not steady",
			mutate: func(sub *operatorsv1alpha1.Subscription, _ *operatorsv1alpha1.ClusterServiceVersion) {
				sub.Status.State = operatorsv1alpha1.SubscriptionStateUpgradeAvailable
			},
		},
		{
			name: "incompatible API service",
			mutate: func(_ *operatorsv1alpha1.Subscription, csv *operatorsv1alpha1.ClusterServiceVersion) {
				csv.Spec.APIServiceDefinitions.Owned = []operatorsv1alpha1.APIServiceDescription{{Name: "v1.widgets"}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, csv := healthySubscriptionFixtures()
			tt.mutate(sub, csv)
			m := migrationTestClient(t, sub, csv)

			if err := m.Migrate(ctx, Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}); err == nil {
				t.Fatal("Migrate() unexpectedly accepted an unsafe operator")
			}
			if err := m.Client.Get(ctx, client.ObjectKeyFromObject(sub), &operatorsv1alpha1.Subscription{}); err != nil {
				t.Fatalf("unsafe migration deleted Subscription: %v", err)
			}
			if err := m.Client.Get(ctx, client.ObjectKeyFromObject(csv), &operatorsv1alpha1.ClusterServiceVersion{}); err != nil {
				t.Fatalf("unsafe migration deleted CSV: %v", err)
			}
			if err := m.Client.Get(ctx, client.ObjectKey{Name: sub.Name}, &ocv1.ClusterExtension{}); err == nil {
				t.Fatal("unsafe migration created a ClusterExtension")
			}
		})
	}
}

func TestRollbackAndCleanupRejectInvalidInputWithoutMutation(t *testing.T) {
	ctx := context.Background()
	installed := &ocv1.ClusterExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name: "installed",
			Annotations: map[string]string{
				MigratedFromSubscriptionAnnotation:    "ns/sub",
				MigrationSubscriptionBackupAnnotation: `{}`,
			},
		},
		Status: ocv1.ClusterExtensionStatus{Conditions: []metav1.Condition{{Type: "Installed", Status: metav1.ConditionTrue}}},
	}
	cos := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "installed-1"}}
	m := migrationTestClient(t, installed, cos)
	if err := m.Rollback(ctx, Options{ClusterExtensionName: "installed"}); err == nil {
		t.Fatal("rollback of Installed=True ClusterExtension unexpectedly succeeded without acknowledgement")
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(installed), &ocv1.ClusterExtension{}); err != nil {
		t.Fatalf("unacknowledged rollback deleted ClusterExtension: %v", err)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(cos), &ocv1.ClusterObjectSet{}); err != nil {
		t.Fatalf("unacknowledged rollback deleted ClusterObjectSet: %v", err)
	}

	invalid := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "invalid", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "not-a-reference"}}}
	sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "sub", Namespace: "ns"}}
	m = migrationTestClient(t, invalid, sub)
	if err := m.CleanupConflict(ctx, "invalid"); err == nil {
		t.Fatal("cleanup accepted an invalid migrated-from-subscription annotation")
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(sub), &operatorsv1alpha1.Subscription{}); err != nil {
		t.Fatalf("invalid conflict cleanup deleted Subscription: %v", err)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(invalid), &ocv1.ClusterExtension{}); err != nil {
		t.Fatalf("invalid conflict cleanup deleted ClusterExtension: %v", err)
	}
}

func TestCreateClusterObjectSetCleansTemporarySecretsOnCollision(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t, establishedClusterObjectSetCRD())
	m.Client = failingMigrationClient{Client: m.Client, failCOSCreate: true}
	object := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "operator-config", "namespace": "ns"},
	}}
	err := m.CreateClusterObjectSet(ctx, Options{SubscriptionName: "sub", SubscriptionNamespace: "ns", SystemNamespace: "olmv1-system"}, &MigrationInfo{
		PackageName: "widgets", BundleName: "widgets.v1", Version: "1.0.0", CollectedObjects: []unstructured.Unstructured{object},
	})
	if err == nil || !apierrors.IsAlreadyExists(err) {
		t.Fatalf("CreateClusterObjectSet() error = %v, want collision", err)
	}
	var secrets corev1.SecretList
	if err := m.Client.List(ctx, &secrets, client.InNamespace("olmv1-system")); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("COS collision left temporary Secret(s): %#v", secrets.Items)
	}
	if err := m.Client.Get(ctx, client.ObjectKey{Name: "sub-1"}, &ocv1.ClusterObjectSet{}); err == nil {
		t.Fatal("COS collision created a ClusterObjectSet")
	}
}

func TestCreateClusterObjectSetPreservesSecretsAfterUnknownCreateOutcome(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t, establishedClusterObjectSetCRD())
	m.Client = failingMigrationClient{Client: m.Client, unknownCOSCreate: true}
	object := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "operator-config", "namespace": "ns"},
	}}
	err := m.CreateClusterObjectSet(ctx, Options{SubscriptionName: "sub", SubscriptionNamespace: "ns", SystemNamespace: "olmv1-system"}, &MigrationInfo{
		PackageName: "widgets", BundleName: "widgets.v1", Version: "1.0.0", CollectedObjects: []unstructured.Unstructured{object},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing automatic cleanup") {
		t.Fatalf("CreateClusterObjectSet() error = %v, want unknown ownership cleanup refusal", err)
	}
	var secrets corev1.SecretList
	if err := m.Client.List(ctx, &secrets, client.InNamespace("olmv1-system")); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) == 0 {
		t.Fatal("unknown COS creation outcome removed Secret references")
	}
}

func TestCreateClusterExtensionAlreadyExistsIsKnownOutcome(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t)
	m.Client = failingMigrationClient{Client: m.Client, failCECreate: true}
	ce, ownershipUnknown, err := m.createClusterExtension(ctx, Options{SubscriptionName: "sub", SubscriptionNamespace: "ns", ClusterExtensionName: "sub"}, &MigrationInfo{PackageName: "widgets"})
	if err == nil || !apierrors.IsAlreadyExists(err) {
		t.Fatalf("createClusterExtension() error = %v, want already exists", err)
	}
	if ce != nil || ownershipUnknown {
		t.Fatalf("createClusterExtension() = ce=%#v, ownershipUnknown=%t, want known non-created outcome", ce, ownershipUnknown)
	}
}

func TestCreateMigrationResourcesCleansTrackedCOSAfterClusterExtensionFailure(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t)
	baseClient := m.Client
	m.Client = failingMigrationClient{Client: baseClient, succeedCOS: true, failCECreate: true}
	object := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "operator-config", "namespace": "ns"},
	}}
	err := m.CreateMigrationResources(ctx, Options{SubscriptionName: "sub", SubscriptionNamespace: "ns", ClusterExtensionName: "sub", SystemNamespace: "olmv1-system"}, &MigrationInfo{
		PackageName: "widgets", BundleName: "widgets.v1", Version: "1.0.0", CollectedObjects: []unstructured.Unstructured{object},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "ClusterExtension creation failed") {
		t.Fatalf("CreateMigrationResources() error = %v, want ClusterExtension creation failure", err)
	}
	m.Client = baseClient
	if err := m.Client.Get(ctx, client.ObjectKey{Name: "sub-1"}, &ocv1.ClusterObjectSet{}); err == nil {
		t.Fatal("ClusterExtension failure left the ClusterObjectSet created by this invocation")
	}
}

func TestCreateClusterObjectSetCleansSecretsAfterReadinessFailure(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t, establishedClusterObjectSetCRD())
	baseClient := m.Client
	m.Client = failingMigrationClient{Client: baseClient, blockCOS: true}
	object := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "operator-config", "namespace": "ns"},
	}}
	err := m.CreateClusterObjectSet(ctx, Options{SubscriptionName: "sub", SubscriptionNamespace: "ns", SystemNamespace: "olmv1-system"}, &MigrationInfo{
		PackageName: "widgets", BundleName: "widgets.v1", Version: "1.0.0", CollectedObjects: []unstructured.Unstructured{object},
	})
	if err == nil {
		t.Fatal("CreateClusterObjectSet() unexpectedly completed without COS status")
	}
	m.Client = baseClient
	var secrets corev1.SecretList
	if err := m.Client.List(ctx, &secrets, client.InNamespace("olmv1-system")); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("COS readiness failure left Secret references: %#v", secrets.Items)
	}
	if err := m.Client.Get(ctx, client.ObjectKey{Name: "sub-1"}, &ocv1.ClusterObjectSet{}); err == nil {
		t.Fatal("COS readiness failure left ClusterObjectSet")
	}
}

func TestRecoverBeforeCEPreservesResourcesItDidNotCreate(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "sub-1-ref", Namespace: "olmv1-system",
		Labels: map[string]string{LabelRevisionName: "sub-1", LabelOwnerName: "sub"},
	}}
	cos := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "sub-1"}}
	m := migrationTestClient(t, secret, cos)
	if err := m.RecoverBeforeCE(ctx, Options{ClusterExtensionName: "sub"}, nil); err == nil {
		t.Fatal("RecoverBeforeCE() unexpectedly succeeded without a backup")
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); err != nil {
		t.Fatalf("RecoverBeforeCE() deleted a pre-existing COS reference Secret: %v", err)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(cos), &ocv1.ClusterObjectSet{}); err != nil {
		t.Fatalf("RecoverBeforeCE() deleted a pre-existing ClusterObjectSet: %v", err)
	}
}

func TestRecoverCreatedMigrationResourcesDeletesOnlyTrackedResources(t *testing.T) {
	ctx := context.Background()
	createdSecret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "created-ref", Namespace: "olmv1-system"}}
	otherSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other-ref", Namespace: "olmv1-system"}}
	createdCOS := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "created-1"}}
	otherCOS := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "other-1"}}
	createdCE := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "created"}}
	m := migrationTestClient(t, &createdSecret, otherSecret, createdCOS, otherCOS, createdCE)

	err := m.recoverCreatedMigrationResources(ctx, Options{}, nil, &createdMigrationResources{
		cos:     createdCOS,
		secrets: []corev1.Secret{createdSecret},
		ce:      createdCE,
	})
	if err == nil {
		t.Fatal("recovery unexpectedly succeeded without a backup")
	}
	for _, object := range []client.Object{&createdSecret, createdCOS, createdCE} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); err == nil {
			t.Fatalf("recovery left created %T %q", object, object.GetName())
		}
	}
	for _, object := range []client.Object{otherSecret, otherCOS} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatalf("recovery deleted untracked %T %q: %v", object, object.GetName(), err)
		}
	}
}

func TestRecoverCreatedMigrationResourcesRefusesUnknownOwnership(t *testing.T) {
	ctx := context.Background()
	cos := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "possibly-foreign-1"}}
	m := migrationTestClient(t, cos)
	err := m.recoverCreatedMigrationResources(ctx, Options{}, nil, &createdMigrationResources{
		cos:              cos,
		ownershipUnknown: true,
	})
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("recovery error = %v, want unknown ownership", err)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(cos), &ocv1.ClusterObjectSet{}); err != nil {
		t.Fatalf("recovery deleted a resource with unknown ownership: %v", err)
	}
}

func TestSplitSubRefRejectsMalformedReferences(t *testing.T) {
	for _, ref := range []string{"", "ns", "ns/", "/sub", "ns/sub/extra", "invalid_namespace/sub", "ns/invalid_name"} {
		if _, _, err := splitSubRef(ref); err == nil {
			t.Fatalf("splitSubRef(%q) unexpectedly succeeded", ref)
		}
	}
	if namespace, name, err := splitSubRef("valid-ns/valid.subscription"); err != nil || namespace != "valid-ns" || name != "valid.subscription" {
		t.Fatalf("splitSubRef(valid) = %q, %q, %v", namespace, name, err)
	}
}

func TestCreateClusterExtensionRejectsNameCollisionWithoutReplacement(t *testing.T) {
	ctx := context.Background()
	existing := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "sub", Annotations: map[string]string{"keep": "existing"}}}
	m := migrationTestClient(t, existing)
	err := m.CreateClusterExtension(ctx, Options{SubscriptionName: "sub", SubscriptionNamespace: "ns"}, &MigrationInfo{PackageName: "widgets"})
	if err == nil {
		t.Fatal("CreateClusterExtension() unexpectedly replaced an existing ClusterExtension")
	}
	var got ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKey{Name: "sub"}, &got); err != nil || got.Annotations["keep"] != "existing" {
		t.Fatalf("existing ClusterExtension changed after collision: %#v, err=%v", got, err)
	}
}

func TestEnsureClusterExtensionAbsent(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t, &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "sub"}})
	if err := m.ensureClusterExtensionAbsent(ctx, "sub"); err == nil {
		t.Fatal("existing ClusterExtension passed pre-migration check")
	}
	if err := m.ensureClusterExtensionAbsent(ctx, "available"); err != nil {
		t.Fatalf("absent ClusterExtension failed pre-migration check: %v", err)
	}
}

func TestMigrateRejectsExistingClusterExtensionWithoutMutation(t *testing.T) {
	ctx := context.Background()
	sub, csv := healthySubscriptionFixtures()
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "sub"}}
	m := migrationTestClient(t, sub, csv, ce)
	if err := m.Migrate(ctx, Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}); err == nil {
		t.Fatal("Migrate() unexpectedly accepted an existing ClusterExtension")
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(sub), &operatorsv1alpha1.Subscription{}); err != nil {
		t.Fatalf("existing ClusterExtension migration deleted Subscription: %v", err)
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(csv), &operatorsv1alpha1.ClusterServiceVersion{}); err != nil {
		t.Fatalf("existing ClusterExtension migration deleted CSV: %v", err)
	}
}

func TestRollbackRejectsMissingAndMalformedBackupsWithoutMutation(t *testing.T) {
	ctx := context.Background()
	for name, annotations := range map[string]map[string]string{
		"missing backup":     {MigratedFromSubscriptionAnnotation: "ns/sub"},
		"malformed backup":   {MigratedFromSubscriptionAnnotation: "ns/sub", MigrationSubscriptionBackupAnnotation: "{"},
		"empty backup":       {MigratedFromSubscriptionAnnotation: "ns/sub", MigrationSubscriptionBackupAnnotation: `{}`},
		"missing source ref": {MigrationSubscriptionBackupAnnotation: `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "sub", Annotations: annotations}}
			cos := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "sub-1"}}
			m := migrationTestClient(t, ce, cos)
			if err := m.Rollback(ctx, Options{ClusterExtensionName: "sub", AcknowledgeInstalled: true}); err == nil {
				t.Fatal("Rollback() unexpectedly accepted invalid backup metadata")
			}
			if err := m.Client.Get(ctx, client.ObjectKeyFromObject(ce), &ocv1.ClusterExtension{}); err != nil {
				t.Fatalf("invalid rollback deleted ClusterExtension: %v", err)
			}
			if err := m.Client.Get(ctx, client.ObjectKeyFromObject(cos), &ocv1.ClusterObjectSet{}); err != nil {
				t.Fatalf("invalid rollback deleted ClusterObjectSet: %v", err)
			}
		})
	}
}
