package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

var (
	convertNamespace          string
	convertAll                bool
	convertDryRun             bool
	convertContinueOnErr      bool
	convertBackupDir          string
	convertDeleteOG           bool
	convertCEName             string
	convertInstallNs          string
	convertAckNamespaceDelete bool

	// Acknowledgment flags
	convertAckWatchScope bool
	convertAckOpCond     bool
	convertAckOLMv0API   bool
	convertAckScopedSA   bool
	convertAckNotSteady  bool
)

var convertCmd = &cobra.Command{
	Use:   "convert [operator-name]",
	Short: "Migrate an OLMv0 operator to OLMv1",
	Long: `Migrates an OLMv0 Subscription/CSV to OLMv1 ClusterExtension/ClusterObjectSet.

Use --dry-run to preview without making changes.
Use --all to migrate all eligible operators.

Target is a Subscription name (with -n namespace), or --all.

Examples:
  migrate-operators-v0-to-v1 convert my-operator -n operators
  migrate-operators-v0-to-v1 convert my-operator -n operators --dry-run
  migrate-operators-v0-to-v1 convert --all
  migrate-operators-v0-to-v1 convert --all --continue-on-error`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConvert,
}

func init() {
	convertCmd.Flags().StringVarP(&convertNamespace, "namespace", "n", "", "Subscription namespace (required without --all)")
	convertCmd.Flags().BoolVar(&convertAll, "all", false, "Migrate all eligible operators")
	convertCmd.Flags().BoolVar(&convertDryRun, "dry-run", false, "Preview what would be migrated without making changes")
	convertCmd.Flags().BoolVar(&convertContinueOnErr, "continue-on-error", false, "Continue migrating other operators when one fails (--all only)")
	convertCmd.Flags().StringVar(&convertBackupDir, "backup", "", "Directory to write OLM resource backups before deletion")
	convertCmd.Flags().BoolVar(&convertDeleteOG, "delete-operatorgroup", false, "Delete the OperatorGroup when no other Subscriptions remain")
	convertCmd.Flags().StringVar(&convertCEName, "ce-name", "", "ClusterExtension name (default: Subscription name)")
	convertCmd.Flags().StringVar(&convertInstallNs, "install-namespace", "", "Install namespace (default: Subscription namespace)")
	convertCmd.Flags().BoolVar(&convertAckNamespaceDelete, "acknowledge-namespace-delete", false, "Delete the source namespace after a cross-namespace migration")
	convertCmd.Flags().BoolVar(&convertAckWatchScope, "acknowledge-watch-scope-change", false, "Acknowledge that the operator will run AllNamespaces (was scoped)")
	convertCmd.Flags().BoolVar(&convertAckOpCond, "acknowledge-operator-condition", false, "Acknowledge active OperatorCondition usage")
	convertCmd.Flags().BoolVar(&convertAckOLMv0API, "acknowledge-olmv0-api-access", false, "Acknowledge OLMv0 API RBAC without OLMv1 equivalent")
	convertCmd.Flags().BoolVar(&convertAckScopedSA, "acknowledge-scoped-serviceaccount", false, "Acknowledge scoped OperatorGroup ServiceAccount (will use cluster-admin)")
	convertCmd.Flags().BoolVar(&convertAckNotSteady, "acknowledge-not-steady-state", false, "Acknowledge that the operator is not at steady state")
}

func runConvert(cmd *cobra.Command, args []string) error { //nolint:nestif
	if convertAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both an operator name and --all")
	}
	if !convertAll && len(args) == 0 {
		return fmt.Errorf("specify an operator name or --all")
	}
	if convertAll && (convertInstallNs != "" || convertAckNamespaceDelete) {
		return fmt.Errorf("--install-namespace and --acknowledge-namespace-delete require a single operator")
	}

	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	m.Progress = progressFunc
	ctx := cmd.Context()

	if convertAll { //nolint:nestif
		fmt.Printf("\n%s%s🔎 Scanning all Subscriptions for migration...%s\n", colorBold, colorCyan, colorReset)
		startProgress()
		results, err := m.ScanAllSubscriptions(ctx)
		clearProgress()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		migration.PrintScanSummary(results, func(format string, a ...interface{}) {
			fmt.Printf(format, a...)
		})

		eligible := migration.EligibleFromScan(results)
		if len(eligible) == 0 {
			info("No eligible operators to migrate.")
			return nil
		}

		fmt.Printf("\n%s%sMigrating %d eligible operator(s)...%s\n", colorBold, colorCyan, len(eligible), colorReset)

		var firstErr error
		for _, r := range eligible {
			info(fmt.Sprintf("Migrating %s/%s...", r.SubscriptionNamespace, r.SubscriptionName))
			opts := migration.Options{
				SubscriptionName:                r.SubscriptionName,
				SubscriptionNamespace:           r.SubscriptionNamespace,
				BackupDirectory:                 convertBackupDir,
				DeleteOperatorGroup:             convertDeleteOG,
				AcknowledgeWatchScopeChange:     convertAckWatchScope,
				AcknowledgeOperatorCondition:    convertAckOpCond,
				AcknowledgeOLMv0APIAccess:       convertAckOLMv0API,
				AcknowledgeScopedServiceAccount: convertAckScopedSA,
				AcknowledgeNotSteadyState:       convertAckNotSteady,
			}
			opts.ApplyDefaults()

			if err := m.Migrate(ctx, opts); err != nil {
				fail(fmt.Sprintf("%s/%s: %v", r.SubscriptionNamespace, r.SubscriptionName, err))
				if !convertContinueOnErr {
					return err
				}
				if firstErr == nil {
					firstErr = err
				}
			} else {
				success(fmt.Sprintf("%s/%s migrated", r.SubscriptionNamespace, r.SubscriptionName))
			}
		}
		return firstErr
	}

	// Single operator
	operatorName := args[0]
	if convertNamespace == "" {
		return fmt.Errorf("-n/--namespace is required")
	}

	opts := migration.Options{
		SubscriptionName:                operatorName,
		SubscriptionNamespace:           convertNamespace,
		ClusterExtensionName:            convertCEName,
		InstallNamespace:                convertInstallNs,
		AcknowledgeNamespaceDelete:      convertAckNamespaceDelete,
		BackupDirectory:                 convertBackupDir,
		DeleteOperatorGroup:             convertDeleteOG,
		AcknowledgeWatchScopeChange:     convertAckWatchScope,
		AcknowledgeOperatorCondition:    convertAckOpCond,
		AcknowledgeOLMv0APIAccess:       convertAckOLMv0API,
		AcknowledgeScopedServiceAccount: convertAckScopedSA,
		AcknowledgeNotSteadyState:       convertAckNotSteady,
	}
	opts.ApplyDefaults()
	if opts.AcknowledgeNamespaceDelete && opts.InstallNamespace == opts.SubscriptionNamespace {
		return fmt.Errorf("--acknowledge-namespace-delete requires --install-namespace to differ from -n/--namespace")
	}

	if convertDryRun {
		return runConvertDryRun(cmd, m, opts)
	}

	fmt.Printf("\n%s%s🔄 Migrating %s/%s to OLMv1...%s\n", colorBold, colorCyan, convertNamespace, operatorName, colorReset)

	// TODO: the step-by-step flow below duplicates some logic from m.Migrate() to provide
	// richer per-step output. Consider adding a progress channel to Options so the library
	// can emit structured events that the CLI can format, avoiding the duplication.
	stepHeader(1, "Profiling operator")
	_, csv, ip, err := m.GetCSVAndInstallPlan(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to profile operator: %w", err)
	}
	bundleInfo, err := m.GetBundleInfo(ctx, opts, csv, ip)
	if err != nil {
		return fmt.Errorf("failed to get bundle info: %w", err)
	}
	detail("Package:", bundleInfo.PackageName)
	detail("Version:", bundleInfo.Version)
	detail("Channel:", valueOrDefault(bundleInfo.Channel, "(default)"))
	success("Operator profiled")

	stepHeader(2, "Checking readiness and compatibility")
	sectionHeader("Readiness")
	readiness, err := m.CheckReadiness(ctx, opts)
	if err != nil {
		return fmt.Errorf("readiness check failed: %w", err)
	}
	printCheckResults(readiness.Checks)

	sectionHeader("Compatibility")
	propsJSON := csv.Annotations["operatorframework.io/properties"]
	compat, err := m.CheckCompatibility(ctx, opts, csv, propsJSON)
	if err != nil {
		return fmt.Errorf("compatibility check failed: %w", err)
	}
	printCheckResults(compat.Checks)

	allFailed := append(readiness.FailedChecks(), compat.FailedChecks()...)
	if len(allFailed) > 0 {
		return fmt.Errorf("operator is not eligible for migration (%d checks failed)", len(allFailed))
	}

	stepHeader(3, "Determining target ClusterCatalog")
	startProgress()
	catalogName, err := m.ResolveClusterCatalog(ctx, bundleInfo, restCfg)
	clearProgress()
	if err != nil {
		var notFound *migration.PackageNotFoundError
		if errors.As(err, &notFound) {
			fail(fmt.Sprintf("No ClusterCatalog found for package %q — run migrate-catalogs-v0-to-v1 first", bundleInfo.PackageName))
		}
		return fmt.Errorf("failed to resolve ClusterCatalog: %w", err)
	}
	bundleInfo.ResolvedCatalogName = catalogName
	success(fmt.Sprintf("Selected ClusterCatalog: %s", catalogName))

	// Verify every OLMv1 prerequisite before deleting the Subscription or CSV.
	// This also discovers the operator-controller namespace used by SecretPacker.
	opts, err = m.PrepareClusterObjectSet(ctx, opts)
	if err != nil {
		return fmt.Errorf("ClusterObjectSet prerequisite check failed: %w", err)
	}
	success(fmt.Sprintf("ClusterObjectSet API established; using operator-controller namespace %s", opts.SystemNamespace))
	if err := m.PrepareInstallNamespace(ctx, opts); err != nil {
		return fmt.Errorf("install namespace preparation failed: %w", err)
	}

	stepHeader(4, "Collecting operator resources")
	objects, err := m.CollectResources(ctx, opts, csv, ip, bundleInfo.PackageName)
	if err != nil {
		return fmt.Errorf("failed to collect resources: %w", err)
	}
	sourceObjects := make([]unstructured.Unstructured, len(objects))
	for i := range objects {
		sourceObjects[i] = *objects[i].DeepCopy()
	}
	migration.RewriteInstallNamespace(objects, opts.SubscriptionNamespace, opts.InstallNamespace)
	bundleInfo.CollectedObjects = objects
	kindCounts := make(map[string]int)
	for _, obj := range objects {
		kindCounts[obj.GetKind()]++
	}
	success(fmt.Sprintf("Found %d resources across %d kinds", len(objects), len(kindCounts)))

	stepHeader(5, "Backing up resources")
	backup, err := m.BackupResources(ctx, opts, csv, ip)
	if err != nil {
		return fmt.Errorf("failed to backup resources: %w", err)
	}
	// Populate CE backup annotations (R2.5) — before PrepareForMigration deletes the Sub.
	if backup.Subscription != nil {
		if j, jErr := json.Marshal(backup.Subscription.Spec); jErr == nil {
			bundleInfo.SubscriptionBackupJSON = string(j)
		}
	}
	if backup.OperatorGroup != nil {
		if j, jErr := json.Marshal(backup.OperatorGroup.Spec); jErr == nil {
			bundleInfo.OperatorGroupBackupJSON = string(j)
		}
	}
	// Disk backup (non-fatal per R2.6).
	if convertBackupDir != "" {
		if err := backup.SaveToDisk(convertBackupDir); err != nil {
			warn(fmt.Sprintf("Backup to disk failed (CE annotation backup is authoritative): %v", err))
		} else {
			success(fmt.Sprintf("Backup written to %s", convertBackupDir))
		}
	}
	success("Resources backed up in memory (CE annotation backup authoritative)")

	stepHeader(6, "Preparing operator for migration")
	info("Deleting Subscription and CSV (orphan cascade)...")
	if err := m.PrepareForMigration(ctx, opts, csv); err != nil {
		return fmt.Errorf("preparation failed: %w", err)
	}
	success("OLMv0 management removed")
	restoreSourceDeployments, err := m.ScaleSourceDeployments(ctx, sourceObjects, opts)
	if err != nil {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if recoverErr := m.RecoverFromBackup(recoveryCtx, opts, backup); recoverErr != nil {
			return fmt.Errorf("scale source Deployments: %w; recovery also failed: %v", err, recoverErr)
		}
		return fmt.Errorf("scale source Deployments failed (recovered): %w", err)
	}
	if opts.InstallNamespace != opts.SubscriptionNamespace {
		success("Source Deployments scaled to zero before target cutover")
	}

	stepHeader(7, "Creating OLMv1 migration resources")
	info(fmt.Sprintf("Applying COS %s-1 with %d objects and creating its ClusterExtension...", opts.ClusterExtensionName, len(bundleInfo.CollectedObjects)))
	startProgress()
	result, err := m.CreateMigrationResources(ctx, opts, bundleInfo, backup)
	if err != nil {
		clearProgress()
		if result.TargetMayBeActive {
			return fmt.Errorf("create migration resources: %w; target ClusterObjectSet may be active, so source Deployments remain scaled to zero", err)
		}
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if restoreErr := restoreSourceDeployments(restoreCtx); restoreErr != nil {
			return fmt.Errorf("create migration resources: %w; restore source Deployments: %v", err, restoreErr)
		}
		return err
	}
	clearProgress()
	success(fmt.Sprintf("ClusterObjectSet %s-1 reached Succeeded=True", opts.ClusterExtensionName))
	success(fmt.Sprintf("ClusterExtension %s is Installed", opts.ClusterExtensionName))
	if err := m.DeleteSourceNamespaceResources(ctx, sourceObjects, opts); err != nil {
		return fmt.Errorf("delete source install resources: %w", err)
	}

	stepHeader(8, "Cleaning up OLMv0 resources")
	cleanupResult := m.CleanupOLMv0Resources(ctx, opts, bundleInfo.PackageName, csv.Name)
	for _, action := range cleanupResult.Actions {
		switch {
		case action.Skipped:
			info(fmt.Sprintf("⏭  %s", action.Description))
		case action.Error != nil:
			warn(fmt.Sprintf("%s: %v", action.Description, action.Error))
		case action.Succeeded:
			success(action.Description)
		}
	}
	if err := cleanupResult.Err(); err != nil {
		return fmt.Errorf("clean up OLMv0 resources: %w", err)
	}
	if err := m.DeleteSourceNamespace(ctx, opts); err != nil {
		return err
	}

	banner(fmt.Sprintf("Migration complete! %s is now managed by OLMv1", bundleInfo.PackageName))
	fmt.Println()
	return nil
}

func runConvertDryRun(cmd *cobra.Command, m *migration.Migrator, opts migration.Options) error {
	ctx := cmd.Context()
	fmt.Printf("\n%s%s🔍 Dry run: %s/%s%s\n", colorBold, colorCyan, opts.SubscriptionNamespace, opts.SubscriptionName, colorReset)

	// Dry-run must reject a target that cannot create a COS, just as a real
	// conversion would. This is read-only and runs before gathering the preview.
	var err error
	opts, err = m.PrepareClusterObjectSet(ctx, opts)
	if err != nil {
		return fmt.Errorf("ClusterObjectSet prerequisite check failed: %w", err)
	}
	success(fmt.Sprintf("ClusterObjectSet API established; using operator-controller namespace %s", opts.SystemNamespace))

	info, err := m.GatherMigrationInfo(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to gather migration info: %w", err)
	}

	success(fmt.Sprintf("Package: %s  Version: %s  Channel: %s", info.PackageName, info.Version, valueOrDefault(info.Channel, "(default)")))
	fmt.Printf("\n  Resources that would be created:\n")
	detail("ClusterObjectSet:", fmt.Sprintf("%s-1 (wait for Succeeded=True before creating the ClusterExtension)", opts.ClusterExtensionName))
	fmt.Printf("\n  Resources that would be placed into ClusterObjectSet %s-1:\n", opts.ClusterExtensionName)

	kindCounts := make(map[string]int)
	for _, obj := range info.CollectedObjects {
		kindCounts[obj.GetKind()]++
	}
	for kind, count := range kindCounts {
		detail(fmt.Sprintf("%s:", kind), fmt.Sprintf("%d object(s)", count))
	}

	fmt.Printf("\n  ClusterExtension that would be created:\n")
	detail("Name:", opts.ClusterExtensionName)
	detail("Namespace:", opts.InstallNamespace)
	detail("PackageName:", info.PackageName)
	if info.ManualApproval {
		detail("Version:", fmt.Sprintf("%s (pinned — manual approval)", info.Version))
	} else {
		detail("Version:", "(unset — automatic channel-based upgrades)")
	}
	detail("Channel:", valueOrDefault(info.Channel, "(none set)"))
	detail("CollisionProtection:", "None")

	fmt.Printf("\n  OLMv0 resources that would be deleted or changed:\n")
	for _, line := range dryRunCleanupPlan(opts, info) {
		info2(line)
	}

	fmt.Printf("\n  Backup plan:\n")
	info2("Store Subscription and OperatorGroup specifications in ClusterExtension annotations before deletion.")
	if opts.BackupDirectory != "" {
		info2(fmt.Sprintf("Write Subscription, OperatorGroup, CSV, and InstallPlan YAML to %s before deletion (not written during dry run).", opts.BackupDirectory))
	}

	fmt.Println()
	info2("No cluster resources were modified (dry run).")
	return nil
}

// dryRunCleanupPlan describes all OLMv0 cleanup actions performed by a normal
// conversion. It intentionally calls no API: dry-run must remain non-mutating.
func dryRunCleanupPlan(opts migration.Options, info *migration.MigrationInfo) []string {
	lines := []string{
		fmt.Sprintf("Delete Subscription %s/%s with orphan propagation (operator workloads remain).", opts.SubscriptionNamespace, opts.SubscriptionName),
		fmt.Sprintf("Delete ClusterServiceVersion %s/%s with orphan propagation (operator workloads remain).", opts.SubscriptionNamespace, info.BundleName),
		fmt.Sprintf("Delete Operator CR %s.%s.", info.PackageName, opts.SubscriptionNamespace),
		fmt.Sprintf("Delete OperatorCondition %s/%s if present.", opts.SubscriptionNamespace, info.BundleName),
		fmt.Sprintf("Delete copied ClusterServiceVersions derived from %s if present, with orphan propagation.", info.BundleName),
		"Retain InstallPlan resources; conversion does not delete them.",
	}
	if opts.DeleteOperatorGroup {
		lines = append(lines, "Delete OperatorGroup(s) only when no Subscriptions remain; strip OLM ownership labels from their aggregation ClusterRoles first.")
	} else {
		lines = append(lines, "Retain OperatorGroup(s); --delete-operatorgroup was not specified.")
	}
	if opts.InstallNamespace != opts.SubscriptionNamespace {
		lines = append(lines,
			fmt.Sprintf("Create or update install namespace %s with PSA/SCC labels copied from %s.", opts.InstallNamespace, opts.SubscriptionNamespace),
			fmt.Sprintf("Move collected namespaced operator resources from %s to %s and delete their source copies after ClusterExtension installation.", opts.SubscriptionNamespace, opts.InstallNamespace))
		if opts.AcknowledgeNamespaceDelete {
			lines = append(lines, fmt.Sprintf("Delete source namespace %s after migration (--acknowledge-namespace-delete).", opts.SubscriptionNamespace))
		} else {
			lines = append(lines, fmt.Sprintf("Retain source namespace %s; --acknowledge-namespace-delete was not specified.", opts.SubscriptionNamespace))
		}
	}
	return lines
}

func info2(msg string) {
	fmt.Printf("  %s\n", msg)
}

func valueOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
