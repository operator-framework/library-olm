package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

var (
	checkSubscriptionName      string
	checkSubscriptionNamespace string
	checkAll                   bool
)

var checkCmd = &cobra.Command{
	Use:   "check [operator-name]",
	Short: "Check readiness and compatibility without performing migration",
	Long: `Runs all pre-migration checks (readiness and compatibility) and reports
any issues that would prevent migration. Does not modify any cluster resources.

Target is a Subscription name (with -n namespace), or --all to scan the cluster.

Examples:
  migrate-operators-v0-to-v1 check my-operator -n operators
  migrate-operators-v0-to-v1 check --all`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCheck,
}

func init() {
	checkCmd.Flags().StringVarP(&checkSubscriptionNamespace, "namespace", "n", "", "Subscription namespace (required without --all)")
	checkCmd.Flags().BoolVar(&checkAll, "all", false, "Check all Subscriptions on the cluster")
}

func runCheck(cmd *cobra.Command, args []string) error {
	if checkAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both an operator name and --all")
	}
	if !checkAll && len(args) == 0 {
		return fmt.Errorf("specify an operator name or --all")
	}

	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	m.Progress = progressFunc
	ctx := cmd.Context()

	if checkAll {
		fmt.Printf("\n%s%s🔎 Scanning all Subscriptions...%s\n", colorBold, colorCyan, colorReset)
		startProgress()
		results, err := m.ScanAllSubscriptions(ctx)
		clearProgress()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}
		migration.PrintScanSummary(results, func(format string, a ...interface{}) {
			fmt.Printf(format, a...)
		})
		return nil
	}

	operatorName := args[0]
	if checkSubscriptionNamespace == "" {
		return fmt.Errorf("-n/--namespace is required")
	}

	fmt.Printf("\n%s%s🔍 Pre-migration checks for %s/%s%s\n", colorBold, colorCyan, checkSubscriptionNamespace, operatorName, colorReset)

	opts := migration.Options{
		SubscriptionName:      operatorName,
		SubscriptionNamespace: checkSubscriptionNamespace,
	}
	opts.ApplyDefaults()

	sectionHeader("Readiness Checks")
	readiness, readinessErr := m.CheckReadiness(ctx, opts)
	if readinessErr != nil {
		fail(fmt.Sprintf("Could not run readiness checks: %v", readinessErr))
	} else {
		printCheckResults(readiness.Checks)
	}

	sectionHeader("Compatibility Checks")
	_, csv, _, profileErr := m.GetCSVAndInstallPlan(ctx, opts)
	if profileErr != nil {
		fail(fmt.Sprintf("Could not profile operator: %v", profileErr))
	} else {
		propsJSON := csv.Annotations["operatorframework.io/properties"]
		compat, compatErr := m.CheckCompatibility(ctx, opts, csv, propsJSON)
		if compatErr != nil {
			fail(fmt.Sprintf("Could not run compatibility checks: %v", compatErr))
		} else {
			printCheckResults(compat.Checks)
		}

		sectionHeader("ClusterCatalog Availability")
		bundleInfo, _ := m.GetBundleInfo(ctx, opts, csv, nil)
		if bundleInfo != nil {
			catalogName, catalogErr := m.ResolveClusterCatalog(ctx, bundleInfo, restCfg)
			if catalogErr != nil {
				var notFound *migration.PackageNotFoundError
				if errors.As(catalogErr, &notFound) {
					warn(fmt.Sprintf("No ClusterCatalog found for package %q — run migrate-catalogs-v0-to-v1 first", bundleInfo.PackageName))
				} else {
					warn(fmt.Sprintf("Catalog resolution error: %v", catalogErr))
				}
			} else {
				success(fmt.Sprintf("ClusterCatalog available: %s", catalogName))
			}
		}
	}

	fmt.Println()
	return nil
}
