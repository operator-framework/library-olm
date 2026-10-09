package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

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
	convertSystemManagedNs    bool
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
Use --all to migrate all eligible operators across all namespaces. The -n/--namespace
and --ce-name flags apply only to a single operator.

Target is a Subscription name (with -n namespace), or --all.

Examples:
  migrate-operators-v0-to-v1 convert my-operator -n operators
  migrate-operators-v0-to-v1 convert my-operator -n operators --dry-run
  migrate-operators-v0-to-v1 convert --all
  migrate-operators-v0-to-v1 convert --all --continue-on-error`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAfterArgumentValidation(validateConvertArguments, runConvert),
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
	convertCmd.Flags().BoolVar(&convertSystemManagedNs, "system-managed-install-namespace", false, "Omit ClusterExtension namespace and let a compatible OLMv1 controller use bundle metadata")
	convertCmd.Flags().BoolVar(&convertAckNamespaceDelete, "acknowledge-namespace-delete", false, "Delete the source namespace after a cross-namespace migration")
	convertCmd.Flags().BoolVar(&convertAckWatchScope, "acknowledge-watch-scope-change", false, "Acknowledge that the operator will run AllNamespaces (was scoped)")
	convertCmd.Flags().BoolVar(&convertAckOpCond, "acknowledge-operator-condition", false, "Acknowledge active OperatorCondition usage")
	convertCmd.Flags().BoolVar(&convertAckOLMv0API, "acknowledge-olmv0-api-access", false, "Acknowledge OLMv0 API RBAC without OLMv1 equivalent")
	convertCmd.Flags().BoolVar(&convertAckScopedSA, "acknowledge-scoped-serviceaccount", false, "Acknowledge scoped OperatorGroup ServiceAccount (will use cluster-admin)")
	convertCmd.Flags().BoolVar(&convertAckNotSteady, "acknowledge-not-steady-state", false, "Acknowledge that the operator is not at steady state")
}

func validateConvertArguments(cmd *cobra.Command, args []string) error {
	if convertAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both an operator name and --all")
	}
	if !convertAll && len(args) == 0 {
		return fmt.Errorf("specify an operator name or --all")
	}
	if convertAll && (convertInstallNs != "" || convertSystemManagedNs || convertAckNamespaceDelete) {
		return fmt.Errorf("--install-namespace, --system-managed-install-namespace, and --acknowledge-namespace-delete require a single operator")
	}
	if convertAll && (convertNamespace != "" || cmd.Flags().Changed("namespace")) {
		return fmt.Errorf("-n/--namespace cannot be combined with --all; --all scans every namespace")
	}
	if convertAll && (convertCEName != "" || cmd.Flags().Changed("ce-name")) {
		return fmt.Errorf("--ce-name cannot be combined with --all; each operator uses its Subscription name")
	}
	if !convertAll {
		if convertNamespace == "" {
			return fmt.Errorf("-n/--namespace is required")
		}
		if convertSystemManagedNs && convertInstallNs != "" {
			return fmt.Errorf("--system-managed-install-namespace cannot be combined with --install-namespace")
		}
		if convertSystemManagedNs && convertAckNamespaceDelete {
			return fmt.Errorf("--system-managed-install-namespace cannot be combined with --acknowledge-namespace-delete")
		}
		if convertAckNamespaceDelete && (convertInstallNs == "" || convertInstallNs == convertNamespace) {
			return fmt.Errorf("--acknowledge-namespace-delete requires --install-namespace to differ from -n/--namespace")
		}
	}
	return nil
}

func runConvert(cmd *cobra.Command, args []string) error { //nolint:nestif
	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	output := selectedCommandOutput()
	m.Progress = progressFuncFor("convert", "")
	ctx := cmd.Context()

	if convertAll { //nolint:nestif
		batchOpts := migration.Options{
			BackupDirectory:                 convertBackupDir,
			DeleteOperatorGroup:             convertDeleteOG,
			AcknowledgeWatchScopeChange:     convertAckWatchScope,
			AcknowledgeOperatorCondition:    convertAckOpCond,
			AcknowledgeOLMv0APIAccess:       convertAckOLMv0API,
			AcknowledgeScopedServiceAccount: convertAckScopedSA,
			AcknowledgeNotSteadyState:       convertAckNotSteady,
		}
		output.scanStart("convert")
		startProgress()
		results, err := m.ScanAllSubscriptionsWithOptions(ctx, batchOpts)
		clearProgress()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		if err := output.scanResults("convert", results); err != nil {
			return err
		}
		if err := progressError(); err != nil {
			return err
		}

		return convertBatch(ctx, results, batchOpts, convertDryRun, convertContinueOnErr, func(ctx context.Context, opts migration.Options) error {
			m.Progress = progressFuncFor("convert", opts.SubscriptionNamespace+"/"+opts.SubscriptionName)
			return m.Migrate(ctx, opts)
		}, func(opts migration.Options) error {
			return runConvertDryRun(cmd, m, opts)
		})
	}

	// Single operator
	operatorName := args[0]

	opts := migration.Options{
		SubscriptionName:                operatorName,
		SubscriptionNamespace:           convertNamespace,
		ClusterExtensionName:            convertCEName,
		InstallNamespace:                convertInstallNs,
		SystemManagedInstallNamespace:   convertSystemManagedNs,
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

	if convertDryRun {
		startProgress()
		err := runConvertDryRun(cmd, m, opts)
		clearProgress()
		if err != nil {
			return err
		}
		if err := progressError(); err != nil {
			return err
		}
		return output.dryRunDone(opts.SubscriptionNamespace + "/" + opts.SubscriptionName)
	}
	m.Progress = progressFuncFor("convert", convertNamespace+"/"+operatorName)

	target := convertNamespace + "/" + operatorName
	output.convertStart(target)
	startProgress()
	err = m.Migrate(ctx, opts)
	clearProgress()
	if err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}
	if err := progressError(); err != nil {
		return err
	}
	return output.convertDone(target, operatorName)
}

func runConvertDryRun(cmd *cobra.Command, m *migration.Migrator, opts migration.Options) error {
	ctx := cmd.Context()
	output := selectedCommandOutput()
	opts.ApplyDefaults()
	m.Progress = progressFuncFor("convert", opts.SubscriptionNamespace+"/"+opts.SubscriptionName)
	target := opts.SubscriptionNamespace + "/" + opts.SubscriptionName
	output.dryRunStart(target)

	resourceOpts := opts
	if opts.SystemManagedInstallNamespace {
		// Check intentionally reports catalog availability before collection.
		// Resolve the metadata-derived namespace first so dry-run preserves the
		// same source-namespace refusal ordering as Migrate.
		_, csv, ip, err := m.GetCSVAndInstallPlan(ctx, opts)
		if err != nil {
			return fmt.Errorf("get CSV for system-managed namespace preview: %w", err)
		}
		bundleInfo, err := m.GetBundleInfo(ctx, opts, csv, ip)
		if err != nil {
			return fmt.Errorf("get bundle info for system-managed namespace preview: %w", err)
		}
		resourceOpts.InstallNamespace, err = opts.EffectiveInstallNamespace(bundleInfo.PackageName, csv.GetAnnotations())
		if err != nil {
			return err
		}
		if resourceOpts.InstallNamespace == opts.SubscriptionNamespace {
			return fmt.Errorf("system-managed install namespace resolves to source namespace %q; use standard migration without --system-managed-install-namespace", opts.SubscriptionNamespace)
		}
	}

	result, err := m.Check(ctx, opts)
	if err != nil {
		return fmt.Errorf("pre-migration check failed: %w", err)
	}
	if result.Status != migration.OperatorStatusEligible {
		if err := output.dryRunChecks(target, *result); err != nil {
			return err
		}
		return fmt.Errorf("operator %s/%s is not eligible for migration (%s): %s", opts.SubscriptionNamespace, opts.SubscriptionName, result.Status, result.Reason)
	}

	if err := output.dryRunChecks(target, *result); err != nil {
		return err
	}
	info, err := m.Gather(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to gather migration info: %w", err)
	}
	return output.dryRunPreview(opts, resourceOpts, info)
}

func dryRunRecord(opts, resourceOpts migration.Options, info *migration.MigrationInfo) outputRecord {
	kindCounts := make(map[string]int)
	for _, obj := range info.CollectedObjects {
		kindCounts[obj.GetKind()]++
	}
	return outputRecord{
		Type: "dry-run", Command: "convert", Target: opts.SubscriptionNamespace + "/" + opts.SubscriptionName,
		Data: dryRunData{
			Package: info.PackageName, Version: info.Version, Channel: info.Channel,
			ClusterObjectSet: opts.ClusterExtensionName + "-1", ClusterExtension: opts.ClusterExtensionName,
			InstallNamespace: resourceOpts.InstallNamespace, SystemNamespace: info.SystemNamespace,
			ManualApproval: info.ManualApproval, KindCounts: kindCounts,
			CleanupActions: dryRunCleanupPlan(opts, resourceOpts, info), BackupDirectory: opts.BackupDirectory,
		},
	}
}

func printDryRunPreview(opts, resourceOpts migration.Options, info *migration.MigrationInfo) error {
	success(fmt.Sprintf("ClusterObjectSet API established; using operator-controller namespace %s", info.SystemNamespace))

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
	if opts.SystemManagedInstallNamespace {
		detail("Namespace:", fmt.Sprintf("(omitted; OLMv1 resolves %s from bundle metadata)", resourceOpts.InstallNamespace))
	} else {
		detail("Namespace:", opts.InstallNamespace)
	}
	detail("PackageName:", info.PackageName)
	if info.ManualApproval {
		detail("Version:", fmt.Sprintf("%s (pinned — manual approval)", info.Version))
	} else {
		detail("Version:", "(unset — automatic channel-based upgrades)")
	}
	detail("Channel:", valueOrDefault(info.Channel, "(none set)"))
	detail("CollisionProtection:", "None")

	fmt.Printf("\n  OLMv0 resources that would be deleted or changed:\n")
	for _, line := range dryRunCleanupPlan(opts, resourceOpts, info) {
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

// dryRunData intentionally excludes collected objects, which may contain Secrets.
type dryRunData struct {
	Package          string         `json:"package"`
	Version          string         `json:"version"`
	Channel          string         `json:"channel,omitempty"`
	ClusterObjectSet string         `json:"cluster_object_set"`
	ClusterExtension string         `json:"cluster_extension"`
	InstallNamespace string         `json:"install_namespace"`
	SystemNamespace  string         `json:"system_namespace"`
	ManualApproval   bool           `json:"manual_approval"`
	KindCounts       map[string]int `json:"kind_counts"`
	CleanupActions   []string       `json:"cleanup_actions"`
	BackupDirectory  string         `json:"backup_directory,omitempty"`
}

// dryRunCleanupPlan describes all OLMv0 cleanup actions performed by a normal
// conversion. It intentionally calls no API: dry-run must remain non-mutating.
func dryRunCleanupPlan(opts, resourceOpts migration.Options, info *migration.MigrationInfo) []string {
	lines := []string{
		fmt.Sprintf("Delete Subscription %s/%s with orphan propagation (operator workloads remain).", opts.SubscriptionNamespace, opts.SubscriptionName),
		fmt.Sprintf("Delete ClusterServiceVersion %s/%s with orphan propagation (operator workloads remain).", opts.SubscriptionNamespace, info.BundleName),
		fmt.Sprintf("Delete Operator CR %s.%s.", info.PackageName, opts.SubscriptionNamespace),
		fmt.Sprintf("Delete OperatorCondition %s/%s if present.", opts.SubscriptionNamespace, info.BundleName),
		fmt.Sprintf("Delete copied ClusterServiceVersions derived from %s if present, with orphan propagation.", info.BundleName),
	}
	deleteSourceNamespace := opts.AcknowledgeNamespaceDelete && resourceOpts.InstallNamespace != opts.SubscriptionNamespace
	if !deleteSourceNamespace {
		lines = append(lines, "Retain InstallPlan resources; conversion does not delete them.")
		if opts.DeleteOperatorGroup {
			lines = append(lines, "Delete OperatorGroup(s) only when no Subscriptions remain; strip OLM ownership labels from their aggregation ClusterRoles first.")
		} else {
			lines = append(lines, "Retain OperatorGroup(s); --delete-operatorgroup was not specified.")
		}
	}
	if resourceOpts.InstallNamespace != opts.SubscriptionNamespace {
		if opts.SystemManagedInstallNamespace {
			lines = append(lines, fmt.Sprintf("Prepare install namespace %s from bundle metadata for the migration COS; ClusterExtension.spec.namespace is omitted and OLMv1 manages the namespace.", resourceOpts.InstallNamespace))
		} else {
			lines = append(lines, fmt.Sprintf("Create or update install namespace %s with PSA/SCC labels copied from %s.", resourceOpts.InstallNamespace, opts.SubscriptionNamespace))
		}
		lines = append(lines, fmt.Sprintf("Move collected namespaced operator resources from %s to %s and delete their source copies after ClusterExtension installation.", opts.SubscriptionNamespace, resourceOpts.InstallNamespace))
		if deleteSourceNamespace {
			lines = append(lines, fmt.Sprintf("Delete source namespace %s after migration (--acknowledge-namespace-delete); this also removes any remaining InstallPlans and OperatorGroups.", opts.SubscriptionNamespace))
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
