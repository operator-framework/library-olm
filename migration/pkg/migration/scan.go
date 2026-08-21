package migration

import (
	"context"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// OperatorScanResult holds the result of scanning a single Subscription for migration eligibility.
type OperatorScanResult struct {
	SubscriptionName      string
	SubscriptionNamespace string
	PackageName           string
	InstalledCSV          string
	Version               string
	State                 string
	Status                OperatorStatus // four-state classification
	Eligible              bool           // true when Status == Eligible (backwards compat)
	Error                 error
	FailedChecks          []CheckResult
}

// ScanAllSubscriptions discovers all Subscriptions on the cluster, checks each for migration
// eligibility, and also detects AlreadyMigrated and Conflict states from ClusterExtensions.
func (m *Migrator) ScanAllSubscriptions(ctx context.Context) ([]OperatorScanResult, error) {
	// List all Subscriptions
	var subList operatorsv1alpha1.SubscriptionList
	if err := m.Client.List(ctx, &subList); err != nil {
		return nil, fmt.Errorf("failed to list Subscriptions: %w", err)
	}

	// List all ClusterExtensions with migrated-from-subscription annotation
	var ceList ocv1.ClusterExtensionList
	if err := m.Client.List(ctx, &ceList); err != nil {
		return nil, fmt.Errorf("failed to list ClusterExtensions: %w", err)
	}

	// Build a map of migration-annotated CEs: "<ns>/<sub-name>" -> CE name
	migratedCEBySubRef := make(map[string]string)
	for _, ce := range ceList.Items {
		if ref, ok := ce.Annotations[MigratedFromSubscriptionAnnotation]; ok {
			migratedCEBySubRef[ref] = ce.Name
		}
	}

	// Build a set of Subscription refs that currently exist
	existingSubs := make(map[string]bool)
	for _, sub := range subList.Items {
		existingSubs[fmt.Sprintf("%s/%s", sub.Namespace, sub.Name)] = true
	}

	var results []OperatorScanResult

	// Check each existing Subscription
	for _, sub := range subList.Items {
		subRef := fmt.Sprintf("%s/%s", sub.Namespace, sub.Name)

		result := OperatorScanResult{
			SubscriptionName:      sub.Name,
			SubscriptionNamespace: sub.Namespace,
			PackageName:           sub.Spec.Package,
			InstalledCSV:          sub.Status.InstalledCSV,
			State:                 string(sub.Status.State),
		}

		// Conflict: both Subscription and annotated CE exist
		if _, hasCE := migratedCEBySubRef[subRef]; hasCE {
			result.Status = OperatorStatusConflict
			result.Eligible = false
			result.Error = fmt.Errorf("both Subscription and annotated ClusterExtension exist; resolve with cleanup or rollback")
			results = append(results, result)
			continue
		}

		opts := Options{
			SubscriptionName:      sub.Name,
			SubscriptionNamespace: sub.Namespace,
		}
		opts.ApplyDefaults()

		m.progress(fmt.Sprintf("Checking %s/%s (%s)...", sub.Namespace, sub.Name, sub.Spec.Package))

		// Readiness checks
		readiness, err := m.CheckReadiness(ctx, opts)
		if err != nil {
			result.Status = OperatorStatusIneligible
			result.Error = err
			results = append(results, result)
			continue
		}

		// Get CSV for compatibility checks
		_, csv, _, err := m.GetCSVAndInstallPlan(ctx, opts)
		if err != nil {
			result.Status = OperatorStatusIneligible
			result.Error = fmt.Errorf("failed to get CSV: %w", err)
			results = append(results, result)
			continue
		}

		result.Version = parseCSVVersion(csv)

		// Compatibility checks
		propsJSON := csv.Annotations["operatorframework.io/properties"]
		compat, err := m.CheckCompatibility(ctx, opts, csv, propsJSON)
		if err != nil {
			result.Status = OperatorStatusIneligible
			result.Error = fmt.Errorf("compatibility check error: %w", err)
			results = append(results, result)
			continue
		}

		// Merge failed checks
		result.FailedChecks = append(readiness.FailedChecks(), compat.FailedChecks()...)
		if len(result.FailedChecks) == 0 {
			result.Status = OperatorStatusEligible
			result.Eligible = true
		} else {
			result.Status = OperatorStatusIneligible
			result.Eligible = false
		}
		results = append(results, result)
	}

	// Check for AlreadyMigrated: CE with annotation but no matching Subscription
	for subRef, ceName := range migratedCEBySubRef {
		if existingSubs[subRef] {
			continue // handled above as Conflict or normal sub
		}
		results = append(results, OperatorScanResult{
			SubscriptionName:      ceName,
			SubscriptionNamespace: "",
			PackageName:           "",
			Status:                OperatorStatusAlreadyMigrated,
			Eligible:              false,
			State:                 fmt.Sprintf("ClusterExtension %s (migrated from %s)", ceName, subRef),
		})
	}

	return results, nil
}

// ScanSubscription checks a single Subscription and returns its scan result.
func (m *Migrator) ScanSubscription(ctx context.Context, opts Options) (*OperatorScanResult, error) {
	opts.ApplyDefaults()

	result := &OperatorScanResult{
		SubscriptionName:      opts.SubscriptionName,
		SubscriptionNamespace: opts.SubscriptionNamespace,
	}

	// Check for Conflict first
	var ceList ocv1.ClusterExtensionList
	if err := m.Client.List(ctx, &ceList); err != nil {
		return nil, fmt.Errorf("failed to list ClusterExtensions: %w", err)
	}
	subRef := fmt.Sprintf("%s/%s", opts.SubscriptionNamespace, opts.SubscriptionName)
	for _, ce := range ceList.Items {
		if ref, ok := ce.Annotations[MigratedFromSubscriptionAnnotation]; ok && ref == subRef {
			result.Status = OperatorStatusConflict
			result.Error = fmt.Errorf("both Subscription and annotated ClusterExtension %s exist; resolve with cleanup or rollback", ce.Name)
			return result, nil
		}
	}

	readiness, err := m.CheckReadiness(ctx, opts)
	if err != nil {
		return nil, err
	}

	_, csv, _, err := m.GetCSVAndInstallPlan(ctx, opts)
	if err != nil {
		result.Status = OperatorStatusIneligible
		result.Error = err
		return result, nil
	}

	result.PackageName = csv.Spec.Description
	result.InstalledCSV = csv.Name
	result.Version = parseCSVVersion(csv)

	propsJSON := csv.Annotations["operatorframework.io/properties"]
	compat, err := m.CheckCompatibility(ctx, opts, csv, propsJSON)
	if err != nil {
		result.Status = OperatorStatusIneligible
		result.Error = err
		return result, nil
	}

	result.FailedChecks = append(readiness.FailedChecks(), compat.FailedChecks()...)
	if len(result.FailedChecks) == 0 {
		result.Status = OperatorStatusEligible
		result.Eligible = true
	} else {
		result.Status = OperatorStatusIneligible
	}
	return result, nil
}

// PrintScanSummary prints results in the required order: Conflict → Ineligible → AlreadyMigrated → Eligible.
func PrintScanSummary(results []OperatorScanResult, printf func(string, ...interface{})) {
	byStatus := make(map[OperatorStatus][]OperatorScanResult)
	for _, r := range results {
		byStatus[r.Status] = append(byStatus[r.Status], r)
	}

	order := []OperatorStatus{
		OperatorStatusConflict,
		OperatorStatusIneligible,
		OperatorStatusAlreadyMigrated,
		OperatorStatusEligible,
	}

	for _, status := range order {
		list := byStatus[status]
		if len(list) == 0 {
			continue
		}
		printf("\n=== %s (%d) ===\n", status, len(list))
		for _, r := range list {
			switch status {
			case OperatorStatusConflict:
				printf("  ⚠️  %s/%s — CONFLICT: %v\n", r.SubscriptionNamespace, r.SubscriptionName, r.Error)
			case OperatorStatusIneligible:
				printf("  ✗ %s/%s", r.SubscriptionNamespace, r.SubscriptionName)
				for _, fc := range r.FailedChecks {
					printf("\n      [%s] %s", fc.Name, fc.Message)
				}
				if r.Error != nil {
					printf("\n      error: %v", r.Error)
				}
				printf("\n")
			case OperatorStatusAlreadyMigrated:
				printf("  ✓ %s (already migrated)\n", r.State)
			case OperatorStatusEligible:
				printf("  ✓ %s/%s (%s)\n", r.SubscriptionNamespace, r.SubscriptionName, r.PackageName)
			}
		}
	}
}

// EligibleFromScan returns only the Eligible results from a scan.
func EligibleFromScan(results []OperatorScanResult) []OperatorScanResult {
	var eligible []OperatorScanResult
	for _, r := range results {
		if r.Status == OperatorStatusEligible {
			eligible = append(eligible, r)
		}
	}
	return eligible
}

// migrationAnnotatedCEsForSub returns ClusterExtension names that are annotated
// with the given subscription ref, or empty if none.
func migrationAnnotatedCEsForSub(ceList *ocv1.ClusterExtensionList, subRef string) []string {
	var names []string
	for _, ce := range ceList.Items {
		if ref, ok := ce.Annotations[MigratedFromSubscriptionAnnotation]; ok && ref == subRef {
			names = append(names, ce.Name)
		}
	}
	return names
}

// RollbackClusterExtension deletes the CE and COS (orphan cascade), then restores the Subscription.
func (m *Migrator) RollbackClusterExtension(ctx context.Context, ceName string, acknowledgeInstalled bool) error {
	var ce ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKey{Name: ceName}, &ce); err != nil {
		return fmt.Errorf("failed to get ClusterExtension %s: %w", ceName, err)
	}

	// Check if Installed=True and require acknowledgment
	if !acknowledgeInstalled {
		for _, cond := range ce.Status.Conditions {
			if cond.Type == "Installed" && cond.Status == "True" {
				return fmt.Errorf("ClusterExtension %s is Installed=True; pass --acknowledge-installed to confirm rollback", ceName)
			}
		}
	}

	// Delete CE (orphan cascade — preserves operator workloads)
	if err := m.Client.Delete(ctx, &ce, client.PropagationPolicy("Orphan")); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete ClusterExtension: %w", err)
		}
	}

	// Delete COS (orphan cascade)
	cosName := fmt.Sprintf("%s-1", ceName)
	var cos ocv1.ClusterObjectSet
	if err := m.Client.Get(ctx, client.ObjectKey{Name: cosName}, &cos); err == nil {
		if err := m.Client.Delete(ctx, &cos, client.PropagationPolicy("Orphan")); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("failed to delete ClusterObjectSet: %w", err)
			}
		}
	}

	// Restore Subscription from backup annotation
	subBackupJSON, ok := ce.Annotations["olm.operatorframework.io/migration-subscription-backup"]
	if !ok || subBackupJSON == "" {
		return fmt.Errorf("ClusterExtension %s has no migration-subscription-backup annotation; cannot restore Subscription", ceName)
	}

	subRef := ce.Annotations[MigratedFromSubscriptionAnnotation]
	if subRef == "" {
		return fmt.Errorf("ClusterExtension %s has no migrated-from-subscription annotation", ceName)
	}

	// Restore Subscription
	var subSpec operatorsv1alpha1.SubscriptionSpec
	if err := unmarshalJSON(subBackupJSON, &subSpec); err != nil {
		return fmt.Errorf("failed to unmarshal subscription backup: %w", err)
	}

	ns, name, err := splitNamespacedName(subRef)
	if err != nil {
		return fmt.Errorf("invalid migrated-from-subscription annotation %q: %w", subRef, err)
	}

	restoredSub := &operatorsv1alpha1.Subscription{}
	restoredSub.Name = name
	restoredSub.Namespace = ns
	restoredSub.Spec = &subSpec

	if err := m.Client.Create(ctx, restoredSub); err != nil {
		return fmt.Errorf("failed to restore Subscription %s/%s: %w", ns, name, err)
	}

	m.progress(fmt.Sprintf("Subscription %s/%s restored; operator returning to OLMv0 management", ns, name))
	return nil
}

// CleanupConflict resolves a Conflict state: deletes the Subscription and OLMv0 artifacts,
// leaving the ClusterExtension intact.
func (m *Migrator) CleanupConflict(ctx context.Context, ceName string) error {
	var ce ocv1.ClusterExtension
	if err := m.Client.Get(ctx, client.ObjectKey{Name: ceName}, &ce); err != nil {
		return fmt.Errorf("failed to get ClusterExtension %s: %w", ceName, err)
	}

	subRef := ce.Annotations[MigratedFromSubscriptionAnnotation]
	if subRef == "" {
		return fmt.Errorf("ClusterExtension %s has no migrated-from-subscription annotation", ceName)
	}

	ns, name, err := splitNamespacedName(subRef)
	if err != nil {
		return fmt.Errorf("invalid migrated-from-subscription annotation %q: %w", subRef, err)
	}

	// Delete Subscription (orphan)
	sub := &operatorsv1alpha1.Subscription{}
	sub.Name = name
	sub.Namespace = ns
	if err := m.Client.Delete(ctx, sub, client.PropagationPolicy("Orphan")); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete Subscription %s/%s: %w", ns, name, err)
		}
	}
	m.progress(fmt.Sprintf("Deleted Subscription %s/%s", ns, name))

	// Cleanup remaining OLMv0 resources
	opts := Options{
		SubscriptionName:      name,
		SubscriptionNamespace: ns,
		ClusterExtensionName:  ceName,
		InstallNamespace:      ns,
	}

	// Try to get package name from CE annotations
	packageName := ce.Spec.Source.Catalog.PackageName
	csvName := "" // best effort
	m.CleanupOLMv0Resources(ctx, opts, packageName, csvName)

	return nil
}

// splitNamespacedName splits "namespace/name" into its components.
func splitNamespacedName(ref string) (string, string, error) {
	for i, c := range ref {
		if c == '/' {
			return ref[:i], ref[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("expected namespace/name format, got %q", ref)
}

func unmarshalJSON(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}
