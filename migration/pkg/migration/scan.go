package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	Reason                string         // human-readable explanation of the status (R1.3)
	Eligible              bool           // true when Status == Eligible (backwards compat)
	// Warnings are informational notices that do not affect eligibility (R9).
	// Example: other installed operators declare a dependency on this package.
	Warnings     []string
	Error        error
	FailedChecks []CheckResult
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
			result.Reason = "both Subscription and annotated ClusterExtension exist; resolve with cleanup or rollback"
			result.Eligible = false
			result.Error = fmt.Errorf("%s", result.Reason)
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
			result.Reason = err.Error()
			result.Error = err
			results = append(results, result)
			continue
		}

		// Get CSV for compatibility checks
		_, csv, _, err := m.GetCSVAndInstallPlan(ctx, opts)
		if err != nil {
			result.Status = OperatorStatusIneligible
			result.Reason = fmt.Sprintf("failed to get CSV: %v", err)
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
			result.Reason = fmt.Sprintf("compatibility check error: %v", err)
			result.Error = fmt.Errorf("compatibility check error: %w", err)
			results = append(results, result)
			continue
		}

		// Merge readiness + compat failed checks
		result.FailedChecks = append(readiness.FailedChecks(), compat.FailedChecks()...)

		// C7: catalog availability (hard check — no override).
		// Only run when readiness+compat pass to avoid noisy catalog errors for clearly ineligible operators.
		if len(result.FailedChecks) == 0 { //nolint:nestif
			catalogName, catalogErr := m.ResolveClusterCatalog(ctx, &MigrationInfo{
				PackageName: sub.Spec.Package,
				Channel:     sub.Spec.Channel,
				Version:     result.Version,
			}, m.RESTConfig)
			if catalogErr != nil {
				result.FailedChecks = append(result.FailedChecks, CheckResult{
					Name:    "Catalog availability",
					Passed:  false,
					Message: fmt.Sprintf("package not found in any serving ClusterCatalog; run migrate-catalogs-v0-to-v1 first: %v", catalogErr),
				})
				result.Status = OperatorStatusIneligible
				result.Reason = fmt.Sprintf("package %q not found in any serving ClusterCatalog", sub.Spec.Package)
				result.Eligible = false
			} else {
				result.FailedChecks = append(result.FailedChecks, CheckResult{
					Name:    "Catalog availability",
					Passed:  true,
					Message: fmt.Sprintf("package available in ClusterCatalog %s", catalogName),
				})
				result.Status = OperatorStatusEligible
				result.Reason = "passes all readiness, compatibility, and catalog-availability checks"
				result.Eligible = true
				// R9: warn if other installed operators declare a dependency on this package.
				if dependents := m.findDependents(ctx, sub.Spec.Package); len(dependents) > 0 {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("other operator(s) may depend on package %q: %v — verify they remain functional after migration", sub.Spec.Package, dependents))
				}
			}
		} else {
			result.Status = OperatorStatusIneligible
			result.Reason = fmt.Sprintf("%d check(s) failed", len(result.FailedChecks))
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
			SubscriptionName: ceName,
			Status:           OperatorStatusAlreadyMigrated,
			Reason:           fmt.Sprintf("ClusterExtension %s exists with migrated-from-subscription annotation; Subscription is gone", ceName),
			Eligible:         false,
			State:            fmt.Sprintf("ClusterExtension %s (migrated from %s)", ceName, subRef),
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
			result.Reason = fmt.Sprintf("both Subscription and annotated ClusterExtension %s exist; resolve with cleanup or rollback", ce.Name)
			result.Error = fmt.Errorf("%s", result.Reason)
			return result, nil
		}
	}

	readiness, err := m.CheckReadiness(ctx, opts)
	if err != nil {
		return nil, err
	}

	sub, csv, _, err := m.GetCSVAndInstallPlan(ctx, opts)
	if err != nil {
		result.Status = OperatorStatusIneligible
		result.Reason = err.Error()
		result.Error = err
		return result, nil
	}

	result.PackageName = sub.Spec.Package
	result.InstalledCSV = csv.Name
	result.Version = parseCSVVersion(csv)

	propsJSON := csv.Annotations["operatorframework.io/properties"]
	compat, err := m.CheckCompatibility(ctx, opts, csv, propsJSON)
	if err != nil {
		result.Status = OperatorStatusIneligible
		result.Reason = err.Error()
		result.Error = err
		return result, nil
	}

	result.FailedChecks = append(readiness.FailedChecks(), compat.FailedChecks()...)
	if len(result.FailedChecks) > 0 {
		result.Status = OperatorStatusIneligible
		result.Reason = fmt.Sprintf("%d check(s) failed", len(result.FailedChecks))
		return result, nil
	}

	// C7: catalog availability (hard check — no override)
	catalogName, catalogErr := m.ResolveClusterCatalog(ctx, &MigrationInfo{
		PackageName: result.PackageName,
		Channel:     sub.Spec.Channel,
		Version:     result.Version,
	}, m.RESTConfig)
	if catalogErr != nil {
		result.FailedChecks = append(result.FailedChecks, CheckResult{
			Name:    "Catalog availability",
			Passed:  false,
			Message: fmt.Sprintf("package not found in any serving ClusterCatalog; run migrate-catalogs-v0-to-v1 first: %v", catalogErr),
		})
		result.Status = OperatorStatusIneligible
		result.Reason = fmt.Sprintf("package %q not found in any serving ClusterCatalog", result.PackageName)
		return result, nil
	}

	result.FailedChecks = append(result.FailedChecks, CheckResult{
		Name:    "Catalog availability",
		Passed:  true,
		Message: fmt.Sprintf("package available in ClusterCatalog %s", catalogName),
	})
	result.Status = OperatorStatusEligible
	result.Reason = "passes all readiness, compatibility, and catalog-availability checks"
	result.Eligible = true
	// R9: warn if other installed operators declare a dependency on this package.
	if dependents := m.findDependents(ctx, result.PackageName); len(dependents) > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("other operator(s) may depend on package %q: %v — verify they remain functional after migration", result.PackageName, dependents))
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

	ns, name, err := splitSubRef(subRef)
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

	ns, name, err := splitSubRef(subRef)
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

// splitSubRef splits a "namespace/name" subscription reference into its components.
func splitSubRef(ref string) (string, string, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid namespace/name ref %q", ref)
	}
	return parts[0], parts[1], nil
}

func unmarshalJSON(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}

// ── Canonical R1.1 library API ────────────────────────────────────────────────

// ScanAll classifies all OLMv0 Subscriptions into the four states (R1.1),
// including catalog-availability (C7) per operator.
func (m *Migrator) ScanAll(ctx context.Context) ([]OperatorScanResult, error) {
	return m.ScanAllSubscriptions(ctx)
}

// Check runs all readiness, compatibility, and catalog-availability checks for
// one operator without mutating the cluster (R1.1).
func (m *Migrator) Check(ctx context.Context, opts Options) (*OperatorScanResult, error) {
	opts.ApplyDefaults()
	return m.ScanSubscription(ctx, opts)
}

// findDependents returns the names of installed operators (Subscription names) whose
// bundle properties declare an olm.package.required dependency on packageName (R9).
// The spec requires a warning — not a block — when migrating an operator others depend on.
func (m *Migrator) findDependents(ctx context.Context, packageName string) []string {
	var subList operatorsv1alpha1.SubscriptionList
	if err := m.Client.List(ctx, &subList); err != nil {
		return nil
	}

	var dependents []string
	for _, sub := range subList.Items {
		if sub.Spec.Package == packageName {
			continue // skip the operator itself
		}
		if sub.Status.InstalledCSV == "" {
			continue
		}
		var csv operatorsv1alpha1.ClusterServiceVersion
		if err := m.Client.Get(ctx, client.ObjectKey{
			Name:      sub.Status.InstalledCSV,
			Namespace: sub.Namespace,
		}, &csv); err != nil {
			continue
		}
		propsJSON := csv.Annotations["operatorframework.io/properties"]
		if propsJSON == "" {
			continue
		}
		props, err := parseProperties(propsJSON)
		if err != nil {
			continue
		}
		for _, p := range props {
			if p.Type == "olm.package.required" {
				var req struct {
					PackageName string `json:"packageName"`
				}
				if err := json.Unmarshal(p.Value, &req); err == nil && req.PackageName == packageName {
					dependents = append(dependents, fmt.Sprintf("%s/%s", sub.Namespace, sub.Name))
					break
				}
			}
		}
	}
	return dependents
}

// Gather collects and returns everything that would be migrated without making
// any cluster mutations — backs the CLI convert --dry-run (R1.1).
func (m *Migrator) Gather(ctx context.Context, opts Options) (*MigrationInfo, error) {
	opts.ApplyDefaults()
	return m.GatherMigrationInfo(ctx, opts)
}

// Rollback restores an operator to OLMv0 management (R1.1).
// opts.AcknowledgeInstalled must be true when the CE is Installed=True.
func (m *Migrator) Rollback(ctx context.Context, opts Options) error {
	opts.ApplyDefaults()
	return m.RollbackClusterExtension(ctx, opts.ClusterExtensionName, opts.AcknowledgeInstalled)
}

// Cleanup finishes a partial migration in Conflict state by deleting the
// Subscription and OLMv0 artifacts, leaving the CE intact (R1.1).
func (m *Migrator) Cleanup(ctx context.Context, opts Options) error {
	opts.ApplyDefaults()
	return m.CleanupConflict(ctx, opts.ClusterExtensionName)
}
