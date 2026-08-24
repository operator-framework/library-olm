package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
	ocv1ac "github.com/operator-framework/operator-controller/applyconfigurations/api/v1"
)

// annotationPrefixesToStrip are annotation prefixes that should be removed from migrated resources.
var annotationPrefixesToStrip = []string{
	"kubectl.kubernetes.io/",
	"olm.operatorframework.io/installed-alongside",
	"deployment.kubernetes.io/",
}

// Migrate performs the full migration of an OLMv0-managed operator to OLMv1.
// Steps:
//  1. Profile the Operator (Subscription/CSV/InstallPlan)
//  2. Determine Compatibility and Readiness
//  3. Determine Target ClusterCatalog
//  4. Backup resources
//  5. Prepare for Migration (delete Sub/CSV with orphan cascade)
//  6. Collect Operator Resources
//  7. Create ClusterObjectSet (wait Succeeded=True)
//  8. Create ClusterExtension (wait Installed=True)
//  9. Clean Up OLMv0 Resources
func (m *Migrator) Migrate(ctx context.Context, opts Options) error {
	opts.ApplyDefaults()

	_, csv, ip, err := m.GetCSVAndInstallPlan(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to profile operator: %w", err)
	}

	info, err := m.GetBundleInfo(ctx, opts, csv, ip)
	if err != nil {
		return fmt.Errorf("failed to get bundle info: %w", err)
	}

	readiness, err := m.CheckReadiness(ctx, opts)
	if err != nil {
		return fmt.Errorf("readiness check failed: %w", err)
	}
	if !readiness.Passed() {
		return fmt.Errorf("readiness checks failed (%d issues)", len(readiness.FailedChecks()))
	}

	propsJSON := csv.Annotations["operatorframework.io/properties"]
	compat, err := m.CheckCompatibility(ctx, opts, csv, propsJSON)
	if err != nil {
		return fmt.Errorf("compatibility check failed: %w", err)
	}
	if !compat.Passed() {
		return fmt.Errorf("operator is not compatible with OLMv1 migration (%d issues found)", len(compat.FailedChecks()))
	}

	catalogName, err := m.ResolveClusterCatalog(ctx, info, m.RESTConfig)
	if err != nil {
		return fmt.Errorf("failed to resolve ClusterCatalog: %w", err)
	}
	info.ResolvedCatalogName = catalogName

	backup, err := m.BackupResources(ctx, opts, csv, ip)
	if err != nil {
		return fmt.Errorf("failed to backup resources: %w", err)
	}

	// Populate CE backup annotations (R2.5) — must happen before PrepareForMigration deletes the Sub.
	if backup.Subscription != nil {
		if j, err := json.Marshal(backup.Subscription.Spec); err == nil {
			info.SubscriptionBackupJSON = string(j)
		}
	}
	if backup.OperatorGroup != nil {
		if j, err := json.Marshal(backup.OperatorGroup.Spec); err == nil {
			info.OperatorGroupBackupJSON = string(j)
		}
	}

	// Disk backup (non-fatal per R2.6 — CE annotation backup is authoritative).
	if opts.BackupDirectory != "" {
		if err := backup.SaveToDisk(opts.BackupDirectory); err != nil {
			m.progress(fmt.Sprintf("Warning: backup to disk failed (CE annotation backup is authoritative): %v", err))
		}
	}

	if err := m.PrepareForMigration(ctx, opts, csv); err != nil {
		if recoverErr := m.RecoverFromBackup(ctx, opts, backup); recoverErr != nil {
			return fmt.Errorf("preparation failed: %w; recovery also failed: %v", err, recoverErr)
		}
		return fmt.Errorf("preparation failed (recovered): %w", err)
	}

	objects, err := m.CollectResources(ctx, opts, csv, ip, info.PackageName)
	if err != nil {
		return fmt.Errorf("failed to collect resources: %w", err)
	}
	info.CollectedObjects = objects

	if err := m.CreateClusterObjectSet(ctx, opts, info); err != nil {
		if recoverErr := m.RecoverBeforeCE(ctx, opts, backup); recoverErr != nil {
			return fmt.Errorf("COS creation failed: %w; recovery also failed: %v", err, recoverErr)
		}
		return fmt.Errorf("COS creation failed (recovered): %w", err)
	}

	if err := m.CreateClusterExtension(ctx, opts, info); err != nil {
		return fmt.Errorf("failed to create ClusterExtension: %w", err)
	}

	m.CleanupOLMv0Resources(ctx, opts, info.PackageName, csv.Name)

	return nil
}

// EnsurePrerequisites verifies that all prerequisites for migration are met.
func (m *Migrator) EnsurePrerequisites(ctx context.Context, opts Options) (*operatorsv1alpha1.ClusterServiceVersion, *operatorsv1alpha1.InstallPlan, *PreMigrationReport, *PreMigrationReport, error) {
	readiness, err := m.CheckReadiness(ctx, opts)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	_, csv, ip, err := m.GetCSVAndInstallPlan(ctx, opts)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	propsJSON := csv.Annotations["operatorframework.io/properties"]
	compat, err := m.CheckCompatibility(ctx, opts, csv, propsJSON)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	return csv, ip, readiness, compat, nil
}

// BackupResources creates in-memory backup copies of the Subscription, CSV, OperatorGroup,
// and InstallPlan for recovery and auditing (R1.8, R2.6).
func (m *Migrator) BackupResources(ctx context.Context, opts Options, csv *operatorsv1alpha1.ClusterServiceVersion, ip *operatorsv1alpha1.InstallPlan) (*Backup, error) {
	var sub operatorsv1alpha1.Subscription
	if err := m.Client.Get(ctx, types.NamespacedName{
		Name:      opts.SubscriptionName,
		Namespace: opts.SubscriptionNamespace,
	}, &sub); err != nil {
		return nil, fmt.Errorf("failed to backup Subscription: %w", err)
	}

	// Best-effort: fetch the OperatorGroup from the Subscription namespace.
	var ogList operatorsv1.OperatorGroupList
	_ = m.Client.List(ctx, &ogList, client.InNamespace(opts.SubscriptionNamespace))
	var og *operatorsv1.OperatorGroup
	if len(ogList.Items) > 0 {
		og = ogList.Items[0].DeepCopy()
	}

	return &Backup{
		Subscription:          sub.DeepCopy(),
		ClusterServiceVersion: csv.DeepCopy(),
		OperatorGroup:         og,
		InstallPlan:           ip,
	}, nil
}

// PrepareForMigration removes OLMv0 management of the operator by deleting
// the Subscription and CSV with orphan cascading (operator workloads keep running).
func (m *Migrator) PrepareForMigration(ctx context.Context, opts Options, csv *operatorsv1alpha1.ClusterServiceVersion) error {
	// Delete Subscription with orphan cascading
	sub := &operatorsv1alpha1.Subscription{}
	sub.Name = opts.SubscriptionName
	sub.Namespace = opts.SubscriptionNamespace
	if err := m.Client.Delete(ctx, sub, client.PropagationPolicy(metav1.DeletePropagationOrphan)); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete Subscription: %w", err)
		}
	}

	// Delete CSV with orphan cascading
	if err := m.Client.Delete(ctx, csv, client.PropagationPolicy(metav1.DeletePropagationOrphan)); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete CSV: %w", err)
		}
	}

	return nil
}

// RecoverFromBackup restores the Subscription from backup after a failed preparation.
func (m *Migrator) RecoverFromBackup(ctx context.Context, opts Options, backup *Backup) error {
	if backup == nil {
		return fmt.Errorf("no backup available for recovery")
	}

	sub := backup.Subscription.DeepCopy()
	sub.ResourceVersion = ""
	sub.UID = ""
	sub.Generation = 0
	sub.CreationTimestamp = metav1.Time{}
	sub.Status = operatorsv1alpha1.SubscriptionStatus{}

	if backup.Subscription.Status.InstalledCSV != "" {
		sub.Spec.StartingCSV = backup.Subscription.Status.InstalledCSV
	}

	if err := m.Client.Create(ctx, sub); err != nil {
		return fmt.Errorf("failed to re-create Subscription: %w", err)
	}

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		var restored operatorsv1alpha1.Subscription
		if err := m.Client.Get(ctx, types.NamespacedName{
			Name:      opts.SubscriptionName,
			Namespace: opts.SubscriptionNamespace,
		}, &restored); err != nil {
			return false, err
		}
		if restored.Status.State == operatorsv1alpha1.SubscriptionStateAtLatest ||
			restored.Status.State == operatorsv1alpha1.SubscriptionStateUpgradePending {
			return true, nil
		}
		m.progress(fmt.Sprintf("Subscription state: %s (waiting for AtLatestKnown)", restored.Status.State))
		return false, nil
	})
}

// RecoverBeforeCE implements recovery when COS creation fails.
// Deletes the failed COS with orphan cascade, then restores the Subscription.
func (m *Migrator) RecoverBeforeCE(ctx context.Context, opts Options, backup *Backup) error {
	cosName := fmt.Sprintf("%s-1", opts.ClusterExtensionName)
	cos := &ocv1.ClusterObjectSet{}
	cos.Name = cosName
	if err := m.Client.Delete(ctx, cos, client.PropagationPolicy(metav1.DeletePropagationOrphan)); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete COS during recovery: %w", err)
		}
	}

	return m.RecoverFromBackup(ctx, opts, backup)
}

// CreateClusterObjectSet builds and creates a COS from the collected resources.
// It uses CollisionProtection=IfNoController so OLMv1 can adopt existing resources (including CRDs).
// The COS is annotated with the source Subscription reference.
//
// TODO(R2.7): when boxcutter phase 2 introduces ClusterObjectDeployment as a replacement or
// complement to ClusterObjectSet, update this function (and its callers) to create whichever
// OLMv1 revision object(s) are appropriate. Track upstream progress at OPRUN-4716 and the
// boxcutter ClusterObjectDeployment design.
func (m *Migrator) CreateClusterObjectSet(ctx context.Context, opts Options, info *MigrationInfo) error {
	cosName := fmt.Sprintf("%s-1", opts.ClusterExtensionName)

	cosObjects := make([]ocv1ac.ClusterObjectSetObjectApplyConfiguration, 0, len(info.CollectedObjects))
	for _, obj := range info.CollectedObjects {
		stripped := stripResource(obj)
		cosObjects = append(cosObjects, *ocv1ac.ClusterObjectSetObject().
			WithObject(stripped).
			WithCollisionProtection(ocv1.CollisionProtectionIfNoController))
	}

	phases := PhaseSort(cosObjects)

	cosSpec := ocv1ac.ClusterObjectSetSpec().
		WithRevision(1).
		WithCollisionProtection(ocv1.CollisionProtectionIfNoController).
		WithLifecycleState(ocv1.ClusterObjectSetLifecycleStateActive).
		WithPhases(phases...)

	cosAnnotations := map[string]string{
		MigratedFromSubscriptionAnnotation: fmt.Sprintf("%s/%s", opts.SubscriptionNamespace, opts.SubscriptionName),
		LabelPackageName:                   info.PackageName,
		LabelBundleName:                    info.BundleName,
		LabelBundleVersion:                 info.Version,
	}
	if info.BundleImage != "" {
		cosAnnotations[LabelBundleReference] = info.BundleImage
	}

	cos := ocv1ac.ClusterObjectSet(cosName).
		WithSpec(cosSpec).
		WithLabels(map[string]string{
			LabelOwnerKind: ocv1.ClusterExtensionKind,
			LabelOwnerName: opts.ClusterExtensionName,
		}).
		WithAnnotations(cosAnnotations)

	cosObj := &ocv1.ClusterObjectSet{}
	cosObj.Name = cosName

	cosData, err := json.Marshal(cos)
	if err != nil {
		return fmt.Errorf("failed to marshal COS: %w", err)
	}

	if err := m.Client.Patch(ctx, cosObj, client.RawPatch(types.ApplyPatchType, cosData),
		client.ForceOwnership, client.FieldOwner(fieldManager)); err != nil {
		return fmt.Errorf("failed to apply ClusterObjectSet: %w", err)
	}

	return m.WaitForCOSSucceeded(ctx, cosName)
}

// WaitForCOSSucceeded waits for the COS to reach Succeeded=True.
func (m *Migrator) WaitForCOSSucceeded(ctx context.Context, cosName string) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		var cos ocv1.ClusterObjectSet
		if err := m.Client.Get(ctx, types.NamespacedName{Name: cosName}, &cos); err != nil {
			m.progress(fmt.Sprintf("Waiting for COS %s (not found yet)", cosName))
			return false, err
		}

		for _, c := range cos.Status.Conditions {
			if c.Type == ocv1.ClusterObjectSetTypeSucceeded && c.Status == metav1.ConditionTrue {
				return true, nil
			}
			if c.Type == ocv1.ClusterObjectSetTypeSucceeded && c.Reason == ocv1.ClusterObjectSetReasonBlocked {
				return false, fmt.Errorf("ClusterObjectSet %s is blocked: %s", cosName, c.Message)
			}
		}

		m.progress(fmt.Sprintf("Waiting for ClusterObjectSet %s to reach Succeeded=True...", cosName))
		return false, nil
	})
}

// CreateClusterExtension creates a CE that adopts the COS (R2.3).
// spec.serviceAccount is NOT set — deprecated and ignored in OLMv1 (R2.5/R7).
// Migration annotations (R2.5) are added for AlreadyMigrated/Conflict detection and rollback.
func (m *Migrator) CreateClusterExtension(ctx context.Context, opts Options, info *MigrationInfo) error {
	// Build annotations (R2.5).
	annotations := map[string]string{
		MigratedFromSubscriptionAnnotation: fmt.Sprintf("%s/%s", opts.SubscriptionNamespace, opts.SubscriptionName),
	}
	if info.SubscriptionBackupJSON != "" {
		annotations[MigrationSubscriptionBackupAnnotation] = info.SubscriptionBackupJSON
	}
	if info.OperatorGroupBackupJSON != "" {
		annotations[MigrationOperatorGroupBackupAnnotation] = info.OperatorGroupBackupJSON
	}
	// Record which eligibility-override flags were acknowledged (audit trail).
	if opts.AcknowledgeWatchScopeChange {
		annotations[AnnotationAcknowledgedPrefix+"watch-scope-change"] = "true"
	}
	if opts.AcknowledgeOperatorCondition {
		annotations[AnnotationAcknowledgedPrefix+"operator-condition"] = "true"
	}
	if opts.AcknowledgeOLMv0APIAccess {
		annotations[AnnotationAcknowledgedPrefix+"olmv0-api-access"] = "true"
	}
	if opts.AcknowledgeScopedServiceAccount {
		annotations[AnnotationAcknowledgedPrefix+"scoped-serviceaccount"] = "true"
	}
	if opts.AcknowledgeNotSteadyState {
		annotations[AnnotationAcknowledgedPrefix+"not-steady-state"] = "true"
	}

	ce := &ocv1.ClusterExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.ClusterExtensionName,
			Annotations: annotations,
		},
		Spec: ocv1.ClusterExtensionSpec{
			Namespace: opts.InstallNamespace,
			// ServiceAccount is deliberately not set — deprecated and ignored in OLMv1.
			Source: ocv1.SourceConfig{
				SourceType: ocv1.SourceTypeCatalog,
				Catalog: &ocv1.CatalogFilter{
					PackageName: info.PackageName,
				},
			},
		},
	}

	// Version pinning: Manual approval → pin to installed version; Automatic → channel-based upgrades.
	if info.ManualApproval {
		ce.Spec.Source.Catalog.Version = info.Version
	}

	if info.Channel != "" {
		ce.Spec.Source.Catalog.Channels = []string{info.Channel}
	}

	if info.ResolvedCatalogName != "" {
		ce.Spec.Source.Catalog.Selector = &metav1.LabelSelector{
			MatchLabels: map[string]string{
				LabelMetadataName: info.ResolvedCatalogName,
			},
		}
	}

	if err := m.Client.Create(ctx, ce); err != nil {
		return fmt.Errorf("failed to create ClusterExtension: %w", err)
	}

	return m.WaitForClusterExtensionInstalled(ctx, opts.ClusterExtensionName)
}

// WaitForClusterExtensionInstalled waits for the CE to reach Installed=True.
func (m *Migrator) WaitForClusterExtensionInstalled(ctx context.Context, ceName string) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		var ce ocv1.ClusterExtension
		if err := m.Client.Get(ctx, types.NamespacedName{Name: ceName}, &ce); err != nil {
			m.progress(fmt.Sprintf("Waiting for CE %s (not found yet)", ceName))
			return false, err
		}

		for _, c := range ce.Status.Conditions {
			if c.Type == ocv1.TypeInstalled && c.Status == metav1.ConditionTrue {
				return true, nil
			}
		}

		m.progress(fmt.Sprintf("Waiting for ClusterExtension %s to reach Installed=True...", ceName))
		return false, nil
	})
}

// CleanupAction describes a single cleanup operation and its result.
type CleanupAction struct {
	Description string
	Succeeded   bool
	Skipped     bool
	Error       error
}

// CleanupResult holds the results of all cleanup operations.
type CleanupResult struct {
	Actions []CleanupAction
}

// CleanupOLMv0Resources removes remaining OLMv0 resources after migration.
func (m *Migrator) CleanupOLMv0Resources(ctx context.Context, opts Options, packageName, csvName string) *CleanupResult {
	result := &CleanupResult{}

	// 1. Delete the Operator CR
	operatorName := fmt.Sprintf("%s.%s", packageName, opts.SubscriptionNamespace)
	err := m.deleteOperatorCR(ctx, packageName, opts.SubscriptionNamespace)
	result.Actions = append(result.Actions, CleanupAction{
		Description: fmt.Sprintf("Delete Operator CR %s", operatorName),
		Succeeded:   err == nil,
		Error:       err,
	})

	// 2. Delete the OperatorCondition
	if csvName != "" {
		err = m.deleteOperatorCondition(ctx, csvName, opts.SubscriptionNamespace)
		result.Actions = append(result.Actions, CleanupAction{
			Description: fmt.Sprintf("Delete OperatorCondition %s/%s", opts.SubscriptionNamespace, csvName),
			Succeeded:   err == nil,
			Error:       err,
		})

		// 3. Delete copied CSVs
		copiedCount, err := m.deleteCopiedCSVs(ctx, csvName)
		if copiedCount > 0 {
			result.Actions = append(result.Actions, CleanupAction{
				Description: fmt.Sprintf("Delete %d copied CSV(s)", copiedCount),
				Succeeded:   err == nil,
				Error:       err,
			})
		} else {
			result.Actions = append(result.Actions, CleanupAction{
				Description: "Delete copied CSVs",
				Skipped:     true,
			})
		}
	}

	// 4. OperatorGroup cleanup
	ogActions := m.cleanupOperatorGroup(ctx, opts)
	result.Actions = append(result.Actions, ogActions...)

	return result
}

func (m *Migrator) deleteCopiedCSVs(ctx context.Context, csvName string) (int, error) {
	var csvList operatorsv1alpha1.ClusterServiceVersionList
	if err := m.Client.List(ctx, &csvList,
		client.MatchingLabels{
			"olm.managed":    "true",
			"olm.copiedFrom": csvName,
		},
	); err != nil {
		return 0, err
	}

	deleted := 0
	for i := range csvList.Items {
		if err := m.Client.Delete(ctx, &csvList.Items[i], client.PropagationPolicy(metav1.DeletePropagationOrphan)); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return deleted, err
			}
		}
		deleted++
	}
	return deleted, nil
}

func (m *Migrator) deleteOperatorCR(ctx context.Context, packageName, namespace string) error {
	operatorName := fmt.Sprintf("%s.%s", packageName, namespace)
	op := &operatorsv1.Operator{}
	op.Name = operatorName
	if err := m.Client.Delete(ctx, op); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

func (m *Migrator) deleteOperatorCondition(ctx context.Context, csvName, namespace string) error {
	oc := &operatorsv1.OperatorCondition{}
	oc.Name = csvName
	oc.Namespace = namespace
	if err := m.Client.Delete(ctx, oc); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// cleanupOperatorGroup deletes the OperatorGroup when both --delete-operatorgroup is set
// AND no other Subscriptions remain in the namespace (R6).
func (m *Migrator) cleanupOperatorGroup(ctx context.Context, opts Options) []CleanupAction {
	var actions []CleanupAction

	// Both conditions required per R6: flag must be set AND no remaining Subscriptions.
	if !opts.DeleteOperatorGroup {
		actions = append(actions, CleanupAction{
			Description: "Delete OperatorGroup (skipped: --delete-operatorgroup not set)",
			Skipped:     true,
		})
		return actions
	}

	var subList operatorsv1alpha1.SubscriptionList
	if err := m.Client.List(ctx, &subList, client.InNamespace(opts.SubscriptionNamespace)); err != nil {
		actions = append(actions, CleanupAction{
			Description: "Check remaining Subscriptions",
			Error:       err,
		})
		return actions
	}

	if len(subList.Items) > 0 {
		actions = append(actions, CleanupAction{
			Description: fmt.Sprintf("Delete OperatorGroup (skipped: %d Subscription(s) remain)", len(subList.Items)),
			Skipped:     true,
		})
		return actions
	}

	var ogList operatorsv1.OperatorGroupList
	if err := m.Client.List(ctx, &ogList, client.InNamespace(opts.SubscriptionNamespace)); err != nil {
		actions = append(actions, CleanupAction{
			Description: "List OperatorGroups",
			Error:       err,
		})
		return actions
	}

	for i := range ogList.Items {
		og := &ogList.Items[i]

		stripped := m.stripOGAggregationClusterRoles(ctx, og.Name)
		for _, name := range stripped {
			actions = append(actions, CleanupAction{
				Description: fmt.Sprintf("Strip OLM labels from aggregation ClusterRole %s", name),
				Succeeded:   true,
			})
		}

		err := m.Client.Delete(ctx, og)
		if err != nil && client.IgnoreNotFound(err) != nil {
			actions = append(actions, CleanupAction{
				Description: fmt.Sprintf("Delete OperatorGroup %s/%s", og.Namespace, og.Name),
				Error:       err,
			})
		} else {
			actions = append(actions, CleanupAction{
				Description: fmt.Sprintf("Delete OperatorGroup %s/%s", og.Namespace, og.Name),
				Succeeded:   true,
			})
		}
	}

	return actions
}

// stripOGAggregationClusterRoles strips olm.owner and olm.managed labels from
// OperatorGroup aggregation ClusterRoles (olm.og.<name>.<view|admin|edit>-<hash>).
func (m *Migrator) stripOGAggregationClusterRoles(ctx context.Context, ogName string) []string {
	prefix := fmt.Sprintf("olm.og.%s.", ogName)

	var crList unstructured.UnstructuredList
	crList.SetAPIVersion("rbac.authorization.k8s.io/v1")
	crList.SetKind("ClusterRoleList")

	if err := m.Client.List(ctx, &crList); err != nil {
		return nil
	}

	var stripped []string
	for _, cr := range crList.Items {
		if !strings.HasPrefix(cr.GetName(), prefix) {
			continue
		}

		lbls := cr.GetLabels()
		if lbls == nil {
			continue
		}

		changed := false
		for _, key := range []string{"olm.owner", "olm.owner.namespace", "olm.owner.kind", "olm.managed"} {
			if _, ok := lbls[key]; ok {
				delete(lbls, key)
				changed = true
			}
		}

		if changed {
			cr.SetLabels(lbls)
			if err := m.Client.Update(ctx, &cr); err == nil {
				stripped = append(stripped, cr.GetName())
			}
		}
	}
	return stripped
}

// FindCRDClusterRoles returns CRD-owned ClusterRoles that are not managed by OLMv1.
func (m *Migrator) FindCRDClusterRoles(ctx context.Context, csvName string) []string {
	var crList unstructured.UnstructuredList
	crList.SetAPIVersion("rbac.authorization.k8s.io/v1")
	crList.SetKind("ClusterRoleList")

	if err := m.Client.List(ctx, &crList); err != nil {
		return nil
	}

	var crdRoles []string
	for _, cr := range crList.Items {
		name := cr.GetName()
		lbls := cr.GetLabels()
		if lbls != nil && lbls["olm.owner"] == csvName {
			for _, suffix := range []string{"-admin", "-edit", "-view", "-crd"} {
				if strings.HasSuffix(name, suffix) {
					crdRoles = append(crdRoles, name)
					break
				}
			}
		}
	}
	return crdRoles
}

// stripResource removes server-side fields from a resource for inclusion in a COS.
func stripResource(obj unstructured.Unstructured) unstructured.Unstructured {
	stripped := unstructured.Unstructured{Object: make(map[string]interface{})}

	stripped.SetAPIVersion(obj.GetAPIVersion())
	stripped.SetKind(obj.GetKind())
	stripped.SetName(obj.GetName())
	if obj.GetNamespace() != "" {
		stripped.SetNamespace(obj.GetNamespace())
	}

	if lbls := obj.GetLabels(); len(lbls) > 0 {
		stripped.SetLabels(lbls)
	}

	if annotations := obj.GetAnnotations(); len(annotations) > 0 {
		filtered := filterAnnotations(annotations)
		if len(filtered) > 0 {
			stripped.SetAnnotations(filtered)
		}
	}

	if spec, ok := obj.Object["spec"]; ok {
		stripped.Object["spec"] = spec
		stripNestedAnnotations(&stripped)
	}

	if data, ok := obj.Object["data"]; ok {
		stripped.Object["data"] = data
	}
	if stringData, ok := obj.Object["stringData"]; ok {
		stripped.Object["stringData"] = stringData
	}

	if rules, ok := obj.Object["rules"]; ok {
		stripped.Object["rules"] = rules
	}

	if roleRef, ok := obj.Object["roleRef"]; ok {
		stripped.Object["roleRef"] = roleRef
	}
	if subjects, ok := obj.Object["subjects"]; ok {
		stripped.Object["subjects"] = subjects
	}

	if webhooks, ok := obj.Object["webhooks"]; ok {
		stripped.Object["webhooks"] = webhooks
	}

	return stripped
}

// filterAnnotations removes annotation prefixes that should not be migrated.
func filterAnnotations(annotations map[string]string) map[string]string {
	filtered := make(map[string]string)
	for k, v := range annotations {
		shouldStrip := false
		for _, prefix := range annotationPrefixesToStrip {
			if strings.HasPrefix(k, prefix) {
				shouldStrip = true
				break
			}
		}
		if !shouldStrip {
			filtered[k] = v
		}
	}
	return filtered
}

// stripNestedAnnotations removes transient annotations from Deployment pod template metadata.
func stripNestedAnnotations(obj *unstructured.Unstructured) {
	templateAnnotations, found, _ := unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
	if found && templateAnnotations != nil {
		filtered := make(map[string]interface{})
		for k, v := range templateAnnotations {
			shouldStrip := false
			for _, prefix := range annotationPrefixesToStrip {
				if strings.HasPrefix(k, prefix) {
					shouldStrip = true
					break
				}
			}
			if !shouldStrip {
				filtered[k] = v
			}
		}
		if len(filtered) > 0 {
			_ = unstructured.SetNestedField(obj.Object, filtered, "spec", "template", "metadata", "annotations")
		} else {
			unstructured.RemoveNestedField(obj.Object, "spec", "template", "metadata", "annotations")
		}
	}
}
