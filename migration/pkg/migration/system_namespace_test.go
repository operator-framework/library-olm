package migration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

func TestOptionsApplyDefaultsPreservesSystemManagedNamespace(t *testing.T) {
	opts := Options{SubscriptionName: "widgets", SubscriptionNamespace: "operators", SystemManagedInstallNamespace: true}
	opts.ApplyDefaults()
	if opts.InstallNamespace != "" {
		t.Fatalf("InstallNamespace = %q, want empty for system-managed mode", opts.InstallNamespace)
	}
	if opts.ClusterExtensionName != "widgets" {
		t.Fatalf("ClusterExtensionName = %q, want default", opts.ClusterExtensionName)
	}
}

func TestEffectiveInstallNamespaceSystemManaged(t *testing.T) {
	tests := []struct {
		name        string
		packageName string
		annotations map[string]string
		want        string
	}{
		{name: "package default", packageName: "widgets", want: "widgets-system"},
		{name: "suggested namespace", packageName: "widgets", annotations: map[string]string{suggestedNamespaceAnnotation: "widgets-managed"}, want: "widgets-managed"},
		{name: "template takes precedence", packageName: "widgets", annotations: map[string]string{
			suggestedNamespaceAnnotation:         "widgets-managed",
			suggestedNamespaceTemplateAnnotation: `{"metadata":{"name":"widgets-template"}}`,
		}, want: "widgets-template"},
		{name: "normalized package", packageName: "Widget.Operator", want: "widget-operator-31z2emwi-system"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (Options{SystemManagedInstallNamespace: true}).EffectiveInstallNamespace(tt.packageName, tt.annotations)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("EffectiveInstallNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEffectiveInstallNamespaceRejectsInvalidTemplate(t *testing.T) {
	_, err := (Options{SystemManagedInstallNamespace: true}).EffectiveInstallNamespace("widgets", map[string]string{
		suggestedNamespaceTemplateAnnotation: `{"metadata":{"name":"INVALID"}}`,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("EffectiveInstallNamespace() error = %v, want invalid namespace", err)
	}
}

func TestPrepareClusterObjectSetSystemManagedNamespaceCapability(t *testing.T) {
	ctx := context.Background()
	opts := Options{
		SubscriptionName:              "widgets",
		SubscriptionNamespace:         "operators",
		SystemManagedInstallNamespace: true,
		SystemNamespace:               "operator-controller",
	}

	m := migrationTestClient(t, establishedClusterObjectSetCRD(), establishedClusterExtensionCRD(false))
	if _, err := m.PrepareClusterObjectSet(ctx, opts); err != nil {
		t.Fatalf("PrepareClusterObjectSet() optional namespace error = %v", err)
	}

	m = migrationTestClient(t, establishedClusterObjectSetCRD(), establishedClusterExtensionCRD(true))
	if _, err := m.PrepareClusterObjectSet(ctx, opts); err == nil || !strings.Contains(err.Error(), "optional spec.namespace") {
		t.Fatalf("PrepareClusterObjectSet() required namespace error = %v, want capability error", err)
	}
}

func TestSystemManagedNamespaceCapabilityUsesV1Schema(t *testing.T) {
	for _, tt := range []struct {
		name          string
		v1Required    bool
		otherRequired bool
		otherFirst    bool
	}{
		{name: "optional v1 after required older version", otherRequired: true, otherFirst: true},
		{name: "optional v1 before required older version", otherRequired: true},
		{name: "required v1 after optional older version", v1Required: true, otherFirst: true},
		{name: "required v1 before optional older version", v1Required: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			crd := establishedClusterExtensionCRD(tt.v1Required)
			other := *crd.Spec.Versions[0].DeepCopy()
			other.Name = "v1alpha1"
			other.Storage = false
			spec := other.Schema.OpenAPIV3Schema.Properties["spec"]
			spec.Required = nil
			if tt.otherRequired {
				spec.Required = []string{"namespace"}
			}
			other.Schema.OpenAPIV3Schema.Properties["spec"] = spec
			if tt.otherFirst {
				crd.Spec.Versions = append([]apiextensionsv1.CustomResourceDefinitionVersion{other}, crd.Spec.Versions...)
			} else {
				crd.Spec.Versions = append(crd.Spec.Versions, other)
			}

			m := migrationTestClient(t, crd)
			err := m.ensureSystemManagedNamespaceSupport(context.Background())
			if tt.v1Required {
				if err == nil || !strings.Contains(err.Error(), "optional spec.namespace") {
					t.Fatalf("ensureSystemManagedNamespaceSupport() error = %v, want v1 capability error", err)
				}
			} else if err != nil {
				t.Fatalf("ensureSystemManagedNamespaceSupport() error = %v, want optional v1 accepted", err)
			}
		})
	}
}

func TestIncludeSystemManagedNamespace(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "widgets",
		Labels:      map[string]string{"pod-security.kubernetes.io/enforce": "baseline"},
		Annotations: map[string]string{"example.com/source": "migration"},
		UID:         types.UID("server-owned"),
	}})
	info := &MigrationInfo{CollectedObjects: []unstructured.Unstructured{{
		Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "widgets"}},
	}}}
	for range 2 {
		if err := m.IncludeSystemManagedNamespace(ctx, info, "widgets"); err != nil {
			t.Fatal(err)
		}
	}
	if len(info.CollectedObjects) != 1 {
		t.Fatalf("collected objects = %d, want one Namespace", len(info.CollectedObjects))
	}
	namespace := info.CollectedObjects[0]
	if namespace.GetKind() != "Namespace" || namespace.GetName() != "widgets" || namespace.GetLabels()["pod-security.kubernetes.io/enforce"] != "baseline" || namespace.GetAnnotations()["example.com/source"] != "migration" {
		t.Fatalf("collected Namespace = %#v", namespace.Object)
	}
	if namespace.GetUID() != "" {
		t.Fatalf("collected Namespace UID = %q, want server metadata omitted", namespace.GetUID())
	}
}

func TestShouldIncludeSystemManagedNamespace(t *testing.T) {
	for _, tt := range []struct {
		name      string
		opts      Options
		installNS string
		want      bool
	}{
		{name: "different target", opts: Options{SystemManagedInstallNamespace: true, SubscriptionNamespace: "operators"}, installNS: "widgets", want: true},
		{name: "source namespace", opts: Options{SystemManagedInstallNamespace: true, SubscriptionNamespace: "operators"}, installNS: "operators"},
		{name: "explicit namespace mode", opts: Options{SubscriptionNamespace: "operators"}, installNS: "widgets"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIncludeSystemManagedNamespace(tt.opts, tt.installNS); got != tt.want {
				t.Fatalf("shouldIncludeSystemManagedNamespace() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSystemManagedClusterExtensionOmitsNamespace(t *testing.T) {
	ctx := context.Background()
	m := migrationTestClient(t)
	m.Client = failingMigrationClient{Client: m.Client, failCEAfterCreate: true}
	_, _, err := m.createClusterExtension(ctx, Options{
		SubscriptionName:              "widgets",
		SubscriptionNamespace:         "operators",
		ClusterExtensionName:          "widgets",
		SystemManagedInstallNamespace: true,
	}, &MigrationInfo{PackageName: "widgets"})
	if err == nil {
		t.Fatal("createClusterExtension() unexpectedly succeeded")
	}

	var ce ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKey{Name: "widgets"}, &ce); err != nil {
		t.Fatalf("get created ClusterExtension: %v", err)
	}
	data, err := json.Marshal(ce)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	spec := document["spec"].(map[string]any)
	if _, found := spec["namespace"]; found {
		t.Fatalf("ClusterExtension JSON unexpectedly contains spec.namespace: %s", data)
	}
	if ce.Spec.Namespace != "" {
		t.Fatalf("ClusterExtension Spec.Namespace = %q, want empty", ce.Spec.Namespace)
	}
	if ce.Spec.Source.Catalog.Version != "" {
		t.Fatalf("automatic approval pinned ClusterExtension version to %q, want channel-based upgrades", ce.Spec.Source.Catalog.Version)
	}
}
