package migration

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

func supportedDeploymentConfigFixture() *operatorsv1alpha1.SubscriptionConfig {
	return &operatorsv1alpha1.SubscriptionConfig{
		NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
		Tolerations:  []corev1.Toleration{{Key: "tier", Operator: corev1.TolerationOpEqual, Value: "infra", Effect: corev1.TaintEffectNoSchedule}},
		Resources:    &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
		EnvFrom:      []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}},
		Env:          []corev1.EnvVar{{Name: "EXAMPLE", Value: "value"}},
		Volumes:      []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}},
		VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/example", ReadOnly: true}},
		Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
			Weight: 1, Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "example.com/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"east"}}}},
		}}}},
		Annotations: map[string]string{"example.com/configured": "true"},
	}
}

func TestGetBundleInfoDropsSelectorButKeepsDeploymentConfig(t *testing.T) {
	sub, csv := healthySubscriptionFixtures()
	sub.Spec.CatalogSource = "catalog"
	sub.Spec.CatalogSourceNamespace = "catalogs"
	sub.Spec.Config = supportedDeploymentConfigFixture()
	sub.Spec.Config.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widgets"}}
	cs := &operatorsv1alpha1.CatalogSource{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "catalogs"},
		Spec: operatorsv1alpha1.CatalogSourceSpec{
			SourceType: operatorsv1alpha1.SourceTypeGrpc,
			Image:      "registry.example/catalog:latest",
		},
	}
	m := migrationTestClient(t, sub, csv, cs)
	var warnings []ProgressEvent
	m.Progress = func(event ProgressEvent) {
		if event.Status == ProgressWarning {
			warnings = append(warnings, event)
		}
	}
	info, err := m.GetBundleInfo(context.Background(), Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace}, csv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.SubscriptionConfig == nil || info.SubscriptionConfig.Selector != nil {
		t.Fatalf("mapped Subscription config retains unsupported selector: %#v", info.SubscriptionConfig)
	}
	wantConfig := supportedDeploymentConfigFixture()
	if !reflect.DeepEqual(info.SubscriptionConfig, wantConfig) {
		t.Fatalf("mapped Subscription config lost supported fields: %#v", info.SubscriptionConfig)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "selector") {
		t.Fatalf("selector warning = %#v", warnings)
	}
	if sub.Spec.Config.Selector == nil {
		t.Fatal("mapping mutated the original Subscription config")
	}
}

func TestClusterExtensionCatalogAndBackupFieldMappings(t *testing.T) {
	for _, tt := range []struct {
		name, channel, wantVersion string
		manual                     bool
	}{
		{name: "manual approval", channel: "stable", wantVersion: "1.2.3", manual: true},
		{name: "automatic approval", channel: "", wantVersion: "", manual: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			m := migrationTestClient(t)
			m.Client = failingMigrationClient{Client: m.Client, failCEAfterCreate: true}
			opts := Options{
				SubscriptionName: "sub", SubscriptionNamespace: "ns", ClusterExtensionName: "widgets", InstallNamespace: "ns",
				AcknowledgeWatchScopeChange: true,
			}
			info := &MigrationInfo{
				PackageName: "widgets", Version: "1.2.3", Channel: tt.channel, ManualApproval: tt.manual,
				ResolvedCatalogName: "widgets-catalog", SubscriptionBackupJSON: `{"package":"widgets"}`,
				OperatorGroupBackupJSON: `{"targetNamespaces":[]}`,
				SubscriptionConfig:      supportedDeploymentConfigFixture(),
			}
			if _, _, err := m.createClusterExtension(ctx, opts, info); err == nil {
				t.Fatal("injected post-create error was not returned")
			}
			var ce ocv1.ClusterExtension
			if err := m.Client.Get(ctx, client.ObjectKey{Name: opts.ClusterExtensionName}, &ce); err != nil {
				t.Fatal(err)
			}
			if ce.Spec.Namespace != "ns" || ce.Spec.Source.SourceType != ocv1.SourceTypeCatalog || ce.Spec.Source.Catalog.PackageName != "widgets" {
				t.Fatalf("ClusterExtension base mapping = %#v", ce.Spec)
			}
			catalog := ce.Spec.Source.Catalog
			if catalog.Version != tt.wantVersion {
				t.Errorf("catalog version = %q, want %q", catalog.Version, tt.wantVersion)
			}
			if tt.channel == "" && len(catalog.Channels) != 0 || tt.channel != "" && !reflect.DeepEqual(catalog.Channels, []string{tt.channel}) {
				t.Errorf("catalog channels = %q, want %q", catalog.Channels, tt.channel)
			}
			if catalog.Selector == nil || catalog.Selector.MatchLabels[LabelMetadataName] != info.ResolvedCatalogName {
				t.Errorf("catalog selector = %#v", catalog.Selector)
			}
			encoded, err := json.Marshal(ce)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			var spec map[string]json.RawMessage
			if err := json.Unmarshal(document["spec"], &spec); err != nil {
				t.Fatal(err)
			}
			if _, found := spec["serviceAccount"]; found {
				t.Errorf("deprecated serviceAccount was set: %s", encoded)
			}
			for key, want := range map[string]string{
				MigratedFromSubscriptionAnnotation:                  "ns/sub",
				MigrationSubscriptionBackupAnnotation:               info.SubscriptionBackupJSON,
				MigrationOperatorGroupBackupAnnotation:              info.OperatorGroupBackupJSON,
				AnnotationAcknowledgedPrefix + "watch-scope-change": "true",
			} {
				if got := ce.Annotations[key]; got != want {
					t.Errorf("annotation %q = %q, want %q", key, got, want)
				}
			}
			var inline map[string]json.RawMessage
			if ce.Spec.Config == nil || ce.Spec.Config.Inline == nil || json.Unmarshal(ce.Spec.Config.Inline.Raw, &inline) != nil {
				t.Fatalf("inline deployment config = %#v", ce.Spec.Config)
			}
			var config operatorsv1alpha1.SubscriptionConfig
			if err := json.Unmarshal(inline["deploymentConfig"], &config); err != nil || !reflect.DeepEqual(&config, info.SubscriptionConfig) {
				t.Errorf("deploymentConfig = %#v, err=%v", config, err)
			}
		})
	}
}
