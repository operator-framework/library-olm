package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

var (
	convertNamespace     string
	convertAll           bool
	convertDryRun        bool
	convertContinueOnErr bool
	convertBackupDir     string
	convertDeleteOG      bool
	convertCEName        string
	convertInstallNs     string

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
		BackupDirectory:                 convertBackupDir,
		AcknowledgeWatchScopeChange:     convertAckWatchScope,
		AcknowledgeOperatorCondition:    convertAckOpCond,
		AcknowledgeOLMv0APIAccess:       convertAckOLMv0API,
		AcknowledgeScopedServiceAccount: convertAckScopedSA,
		AcknowledgeNotSteadyState:       convertAckNotSteady,
	}
	opts.ApplyDefaults()

	if convertDryRun {
		return runConvertDryRun(cmd, m, opts)
	}

	fmt.Printf("\n%s%s🔄 Migrating %s/%s to OLMv1...%s\n", colorBold, colorCyan, convertNamespace, operatorName, colorReset)

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

	stepHeader(4, "Collecting operator resources")
	objects, err := m.CollectResources(ctx, opts, csv, ip, bundleInfo.PackageName)
	if err != nil {
		return fmt.Errorf("failed to collect resources: %w", err)
	}
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
	info("Deleting Subscription and CSV (orphan cascade — workloads keep running)...")
	if err := m.PrepareForMigration(ctx, opts, csv); err != nil {
		return fmt.Errorf("preparation failed: %w", err)
	}
	success("OLMv0 management removed")

	stepHeader(7, "Creating ClusterObjectSet")
	info(fmt.Sprintf("Applying COS %s-1 with %d objects...", opts.ClusterExtensionName, len(bundleInfo.CollectedObjects)))
	startProgress()
	if err := m.CreateClusterObjectSet(ctx, opts, bundleInfo); err != nil {
		clearProgress()
		return fmt.Errorf("COS creation failed: %w", err)
	}
	clearProgress()
	success(fmt.Sprintf("ClusterObjectSet %s-1 reached Succeeded=True", opts.ClusterExtensionName))

	stepHeader(8, "Creating ClusterExtension")
	startProgress()
	if err := m.CreateClusterExtension(ctx, opts, bundleInfo); err != nil {
		clearProgress()
		return fmt.Errorf("failed to create ClusterExtension: %w", err)
	}
	clearProgress()
	success(fmt.Sprintf("ClusterExtension %s is Installed", opts.ClusterExtensionName))

	stepHeader(9, "Cleaning up OLMv0 resources")
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

	banner(fmt.Sprintf("Migration complete! %s is now managed by OLMv1", bundleInfo.PackageName))
	fmt.Println()
	return nil
}

func runConvertDryRun(cmd *cobra.Command, m *migration.Migrator, opts migration.Options) error {
	ctx := cmd.Context()
	fmt.Printf("\n%s%s🔍 Dry run: %s/%s%s\n", colorBold, colorCyan, opts.SubscriptionNamespace, opts.SubscriptionName, colorReset)

	info, err := m.GatherMigrationInfo(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to gather migration info: %w", err)
	}

	success(fmt.Sprintf("Package: %s  Version: %s  Channel: %s", info.PackageName, info.Version, valueOrDefault(info.Channel, "(default)")))
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
	detail("CollisionProtection:", "IfNoController")

	fmt.Println()
	info2("No cluster resources were modified (dry run).")
	return nil
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
