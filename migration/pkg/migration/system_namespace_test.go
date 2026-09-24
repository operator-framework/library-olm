package migration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
}
