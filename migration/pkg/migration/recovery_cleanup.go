package migration

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// RecoveryCleanupOptions controls explicit removal of migration recovery data.
// IncludeMigrationMetadata also removes the CE's invocation and acknowledgment
// annotations. The migrated-from-subscription marker is always retained.
type RecoveryCleanupOptions struct {
	ClusterExtensionName     string
	AcknowledgeRollbackLoss  bool
	IncludeMigrationMetadata bool
	DryRun                   bool
}

// RecoveryCleanupResult describes the exact CE annotations selected for removal.
// RollbackLoss is true when this operation removes the Subscription backup used
// by the rollback command. An empty Annotations slice means the operation is a no-op.
type RecoveryCleanupResult struct {
	ClusterExtensionName string   `json:"cluster_extension_name"`
	Annotations          []string `json:"annotations"`
	DryRun               bool     `json:"dry_run"`
	RollbackLoss         bool     `json:"rollback_loss"`
}

// CleanupRecoveryAnnotations removes recovery annotations from a fully adopted
// ClusterExtension. It never changes COD templates, COS revisions, packed Secrets,
// provenance, or controller metadata. DryRun performs the same safety checks and
// returns the planned annotation keys without mutating the cluster. Removing a
// present Subscription backup requires AcknowledgeRollbackLoss and disables the
// library's rollback command for this ClusterExtension.
func (m *Migrator) CleanupRecoveryAnnotations(ctx context.Context, opts RecoveryCleanupOptions) (*RecoveryCleanupResult, error) {
	if opts.ClusterExtensionName == "" {
		return nil, fmt.Errorf("ClusterExtension name is required")
	}
	m.progress(ProgressEvent{Step: ProgressStepCleanup, Status: ProgressStarted, Message: fmt.Sprintf("Checking recovery cleanup for ClusterExtension %s", opts.ClusterExtensionName)})
	ce, err := m.recoveryCleanupTarget(ctx, opts.ClusterExtensionName)
	if err != nil {
		return nil, err
	}
	result := &RecoveryCleanupResult{ClusterExtensionName: ce.Name, DryRun: opts.DryRun}
	for _, key := range []string{MigrationSubscriptionBackupAnnotation, MigrationOperatorGroupBackupAnnotation} {
		if _, exists := ce.Annotations[key]; exists {
			result.Annotations = append(result.Annotations, key)
		}
	}
	if opts.IncludeMigrationMetadata {
		for key := range ce.Annotations {
			if key == migrationInvocationAnnotation || strings.HasPrefix(key, AnnotationAcknowledgedPrefix) {
				result.Annotations = append(result.Annotations, key)
			}
		}
	}
	slices.Sort(result.Annotations)
	_, result.RollbackLoss = ce.Annotations[MigrationSubscriptionBackupAnnotation]
	if opts.DryRun {
		m.progress(ProgressEvent{Step: ProgressStepCleanup, Status: ProgressCompleted, Message: fmt.Sprintf("Previewed %d recovery annotations on ClusterExtension %s", len(result.Annotations), ce.Name)})
		return result, nil
	}
	if result.RollbackLoss && !opts.AcknowledgeRollbackLoss {
		return nil, fmt.Errorf("removing recovery annotations disables rollback for ClusterExtension %s; pass --acknowledge-rollback-loss", ce.Name)
	}
	if len(result.Annotations) == 0 {
		m.progress(ProgressEvent{Step: ProgressStepCleanup, Status: ProgressCompleted, Message: fmt.Sprintf("ClusterExtension %s has no selected recovery annotations", ce.Name)})
		return result, nil
	}
	before := ce.DeepCopy()
	ce.Annotations = maps.Clone(ce.Annotations)
	for _, key := range result.Annotations {
		delete(ce.Annotations, key)
	}
	if err := m.Client.Patch(ctx, ce, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return nil, fmt.Errorf("remove recovery annotations from ClusterExtension %s: %w", ce.Name, err)
	}
	m.progress(ProgressEvent{Step: ProgressStepCleanup, Status: ProgressCompleted, Message: fmt.Sprintf("Removed %d recovery annotations from ClusterExtension %s", len(result.Annotations), ce.Name)})
	return result, nil
}

func (m *Migrator) recoveryCleanupTarget(ctx context.Context, name string) (*ocv1.ClusterExtension, error) {
	var ce ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKey{Name: name}, &ce); err != nil {
		return nil, fmt.Errorf("get ClusterExtension %s for recovery cleanup: %w", name, err)
	}
	if ce.DeletionTimestamp != nil {
		return nil, fmt.Errorf("ClusterExtension %s is being deleted", name)
	}
	ref := ce.Annotations[MigratedFromSubscriptionAnnotation]
	ns, subName, err := splitSubRef(ref)
	if err != nil {
		return nil, fmt.Errorf("cluster extension %s has no valid migration provenance: %w", name, err)
	}
	installed := apimeta.FindStatusCondition(ce.Status.Conditions, ocv1.ClusterExtensionTypeInstalled)
	if installed == nil || installed.Status != metav1.ConditionTrue || installed.ObservedGeneration != ce.Generation {
		return nil, fmt.Errorf("ClusterExtension %s is not installed at its current generation", name)
	}
	var sub operatorsv1alpha1.Subscription
	if err := m.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: subName}, &sub); err == nil {
		return nil, fmt.Errorf("subscription %s/%s still exists; resolve the conflict before recovery cleanup", ns, subName)
	} else if !apierrors.IsNotFound(err) && !apimeta.IsNoMatchError(err) {
		return nil, fmt.Errorf("check source Subscription %s/%s: %w", ns, subName, err)
	}
	var cod ocv1.ClusterObjectDeployment
	if err := m.Client.Get(ctx, client.ObjectKey{Name: name}, &cod); err != nil {
		return nil, fmt.Errorf("get ClusterObjectDeployment %s for recovery cleanup: %w", name, err)
	}
	owner := metav1.GetControllerOf(&cod)
	if cod.DeletionTimestamp != nil || owner == nil || owner.APIVersion != ocv1.GroupVersion.String() || owner.Kind != ocv1.ClusterExtensionKind || owner.Name != ce.Name || owner.UID != ce.UID {
		return nil, fmt.Errorf("ClusterObjectDeployment %s is not controlled by ClusterExtension %s", name, name)
	}
	if cod.Labels[controllerManagedByLabel] != "operator-controller" || cod.Spec.Template.Spec.CollisionProtection != ocv1.CollisionProtectionPrevent {
		return nil, fmt.Errorf("ClusterObjectDeployment %s has not completed protected adoption", name)
	}
	progress := apimeta.FindStatusCondition(cod.Status.Conditions, ocv1.ClusterObjectDeploymentTypeProgressing)
	available := apimeta.FindStatusCondition(cod.Status.Conditions, ocv1.ClusterObjectDeploymentTypeAvailable)
	if progress == nil || progress.ObservedGeneration != cod.Generation || progress.Status != metav1.ConditionFalse || progress.Reason != ocv1.ClusterObjectDeploymentReasonNewClusterObjectSetProgressed || available == nil || available.ObservedGeneration != cod.Generation || available.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("ClusterObjectDeployment %s is not ready at its current generation", name)
	}
	return &ce, nil
}
