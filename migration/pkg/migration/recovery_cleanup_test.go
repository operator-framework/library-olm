package migration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

func recoveryCleanupFixtures() (*ocv1.ClusterExtension, *ocv1.ClusterObjectDeployment) {
	ce := &ocv1.ClusterExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name: "widgets", UID: types.UID("ce-uid"), Generation: 1,
			Annotations: map[string]string{
				MigratedFromSubscriptionAnnotation:                  "ns/widgets",
				MigrationSubscriptionBackupAnnotation:               `{"name":"widgets"}`,
				MigrationOperatorGroupBackupAnnotation:              `{"name":"operators"}`,
				migrationInvocationAnnotation:                       "invocation",
				AnnotationAcknowledgedPrefix + "watch-scope-change": "true",
				"example.com/keep":                                  "value",
			},
		},
		Status: ocv1.ClusterExtensionStatus{Conditions: []metav1.Condition{{Type: ocv1.ClusterExtensionTypeInstalled, Status: metav1.ConditionTrue, ObservedGeneration: 1}}},
	}
	controller := true
	cod := &ocv1.ClusterObjectDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "widgets", UID: types.UID("cod-uid"), Generation: 1,
			Annotations:     map[string]string{MigrationSubscriptionBackupAnnotation: "staging backup"},
			Labels:          map[string]string{controllerManagedByLabel: "operator-controller"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: ocv1.GroupVersion.String(), Kind: ocv1.ClusterExtensionKind, Name: ce.Name, UID: ce.UID, Controller: &controller}},
		},
		Spec: ocv1.ClusterObjectDeploymentSpec{Template: ocv1.ClusterObjectDeploymentTemplate{Spec: ocv1.ClusterObjectDeploymentTemplateSpec{}}},
		Status: ocv1.ClusterObjectDeploymentStatus{Conditions: []metav1.Condition{
			{Type: ocv1.ClusterObjectDeploymentTypeProgressing, Status: metav1.ConditionFalse, Reason: ocv1.ClusterObjectDeploymentReasonNewClusterObjectSetProgressed, ObservedGeneration: 1},
			{Type: ocv1.ClusterObjectDeploymentTypeAvailable, Status: metav1.ConditionTrue, ObservedGeneration: 1},
		}},
	}
	cod.Spec.Template.Spec.CollisionProtection = ocv1.CollisionProtectionPrevent
	return ce, cod
}

func TestCleanupRecoveryAnnotationsPreviewAndAcknowledgment(t *testing.T) {
	ce, cod := recoveryCleanupFixtures()
	m := migrationTestClient(t, ce, cod)
	ctx := context.Background()
	var events []ProgressEvent
	m.Progress = func(event ProgressEvent) { events = append(events, event) }
	preview, err := m.CleanupRecoveryAnnotations(ctx, RecoveryCleanupOptions{ClusterExtensionName: ce.Name, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{MigrationOperatorGroupBackupAnnotation, MigrationSubscriptionBackupAnnotation}
	if !reflect.DeepEqual(preview.Annotations, wantKeys) || !preview.RollbackLoss || !preview.DryRun {
		t.Fatalf("preview = %#v, want backup keys and rollback-loss warning", preview)
	}
	var unchanged ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKey{Name: ce.Name}, &unchanged); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unchanged.Annotations, ce.Annotations) {
		t.Fatal("dry-run changed ClusterExtension annotations")
	}
	if _, err := m.CleanupRecoveryAnnotations(ctx, RecoveryCleanupOptions{ClusterExtensionName: ce.Name}); err == nil || !strings.Contains(err.Error(), "--acknowledge-rollback-loss") {
		t.Fatalf("cleanup without acknowledgment = %v", err)
	}
	result, err := m.CleanupRecoveryAnnotations(ctx, RecoveryCleanupOptions{ClusterExtensionName: ce.Name, AcknowledgeRollbackLoss: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Annotations, wantKeys) || result.DryRun {
		t.Fatalf("cleanup result = %#v", result)
	}
	var updated ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKey{Name: ce.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	for _, key := range wantKeys {
		if _, found := updated.Annotations[key]; found {
			t.Fatalf("backup annotation %s remains", key)
		}
	}
	for _, key := range []string{MigratedFromSubscriptionAnnotation, migrationInvocationAnnotation, AnnotationAcknowledgedPrefix + "watch-scope-change", "example.com/keep"} {
		if updated.Annotations[key] != ce.Annotations[key] {
			t.Fatalf("annotation %s was changed", key)
		}
	}
	var unchangedCOD ocv1.ClusterObjectDeployment
	if err := m.Client.Get(ctx, client.ObjectKey{Name: cod.Name}, &unchangedCOD); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unchangedCOD.Annotations, cod.Annotations) || !reflect.DeepEqual(unchangedCOD.Spec, cod.Spec) {
		t.Fatal("cleanup modified the controller-owned COD")
	}
	if len(events) == 0 || events[len(events)-1].Status != ProgressCompleted {
		t.Fatalf("cleanup progress events = %#v", events)
	}
	if err := m.RollbackClusterExtension(ctx, ce.Name, true); err == nil || !strings.Contains(err.Error(), "no migration-subscription-backup") {
		t.Fatalf("rollback after cleanup = %v, want missing-backup refusal", err)
	}
}

func TestCleanupRecoveryAnnotationsAdditionalMetadataAndRetry(t *testing.T) {
	ce, cod := recoveryCleanupFixtures()
	m := migrationTestClient(t, ce, cod)
	opts := RecoveryCleanupOptions{ClusterExtensionName: ce.Name, IncludeMigrationMetadata: true, AcknowledgeRollbackLoss: true}
	result, err := m.CleanupRecoveryAnnotations(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Annotations) != 4 {
		t.Fatalf("removed annotations = %v, want backups, invocation, and audit", result.Annotations)
	}
	var updated ocv1.ClusterExtension
	if err := m.Client.Get(context.Background(), client.ObjectKey{Name: ce.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Annotations) != 2 || updated.Annotations[MigratedFromSubscriptionAnnotation] != "ns/widgets" || updated.Annotations["example.com/keep"] != "value" {
		t.Fatalf("remaining annotations = %v", updated.Annotations)
	}
	result, err = m.CleanupRecoveryAnnotations(context.Background(), RecoveryCleanupOptions{ClusterExtensionName: ce.Name, IncludeMigrationMetadata: true})
	if err != nil || len(result.Annotations) != 0 || result.RollbackLoss {
		t.Fatalf("idempotent retry = %#v, %v", result, err)
	}
}

func TestCleanupRecoveryAnnotationsRejectsUnsafeState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*ocv1.ClusterExtension, *ocv1.ClusterObjectDeployment) []client.Object
	}{
		{name: "missing provenance", modify: func(ce *ocv1.ClusterExtension, _ *ocv1.ClusterObjectDeployment) []client.Object {
			delete(ce.Annotations, MigratedFromSubscriptionAnnotation)
			return nil
		}},
		{name: "source Subscription exists", modify: func(_ *ocv1.ClusterExtension, _ *ocv1.ClusterObjectDeployment) []client.Object {
			return []client.Object{&operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Namespace: "ns"}}}
		}},
		{name: "CE not installed", modify: func(ce *ocv1.ClusterExtension, _ *ocv1.ClusterObjectDeployment) []client.Object {
			ce.Status.Conditions[0].Status = metav1.ConditionFalse
			return nil
		}},
		{name: "foreign COD", modify: func(_ *ocv1.ClusterExtension, cod *ocv1.ClusterObjectDeployment) []client.Object {
			cod.OwnerReferences[0].UID = "foreign"
			return nil
		}},
		{name: "unprotected COD", modify: func(_ *ocv1.ClusterExtension, cod *ocv1.ClusterObjectDeployment) []client.Object {
			cod.Spec.Template.Spec.CollisionProtection = ocv1.CollisionProtectionNone
			return nil
		}},
		{name: "stale COD status", modify: func(_ *ocv1.ClusterExtension, cod *ocv1.ClusterObjectDeployment) []client.Object {
			cod.Generation = 2
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ce, cod := recoveryCleanupFixtures()
			objects := make([]client.Object, 0, 3)
			objects = append(objects, ce, cod)
			objects = append(objects, tc.modify(ce, cod)...)
			m := migrationTestClient(t, ce, cod)
			for _, object := range objects[2:] {
				if err := m.Client.Create(context.Background(), object); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.CleanupRecoveryAnnotations(context.Background(), RecoveryCleanupOptions{ClusterExtensionName: ce.Name, AcknowledgeRollbackLoss: true}); err == nil {
				t.Fatal("unsafe cleanup was accepted")
			}
			var unchanged ocv1.ClusterExtension
			if err := m.Client.Get(context.Background(), client.ObjectKey{Name: ce.Name}, &unchanged); err != nil {
				t.Fatal(err)
			}
			if unchanged.Annotations[MigrationSubscriptionBackupAnnotation] != ce.Annotations[MigrationSubscriptionBackupAnnotation] {
				t.Fatal("unsafe cleanup removed the backup")
			}
		})
	}
}
