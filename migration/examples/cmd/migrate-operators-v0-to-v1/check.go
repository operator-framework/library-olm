package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

var (
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

func runCheck(cmd *cobra.Command, args []string) error { //nolint:nestif
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
	m.Progress = progressFuncFor("check", "")
	ctx := cmd.Context()

	if checkAll {
		if !structuredOutput() {
			fmt.Printf("\n%s%s🔎 Scanning all Subscriptions...%s\n", colorBold, colorCyan, colorReset)
		}
		startProgress()
		results, err := m.ScanAll(ctx)
		clearProgress()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}
		if structuredOutput() {
			return writeOutputRecord(outputRecord{Type: "scan", Command: "check", Data: scanResultsData(results)})
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

	if !structuredOutput() {
		fmt.Printf("\n%s%s🔍 Pre-migration checks for %s/%s%s\n", colorBold, colorCyan, checkSubscriptionNamespace, operatorName, colorReset)
	}

	opts := migration.Options{
		SubscriptionName:      operatorName,
		SubscriptionNamespace: checkSubscriptionNamespace,
	}
	opts.ApplyDefaults()
	m.Progress = progressFuncFor("check", checkSubscriptionNamespace+"/"+operatorName)

	result, err := m.Check(ctx, opts)
	if err != nil {
		return fmt.Errorf("pre-migration check failed: %w", err)
	}
	if structuredOutput() {
		return writeOutputRecord(outputRecord{Type: "check", Command: "check", Target: checkSubscriptionNamespace + "/" + operatorName, Data: scanResultData(*result)})
	}
	sectionHeader("Readiness, Compatibility and Catalog Checks")
	printCheckResults(result.Checks)
	if result.Status == migration.OperatorStatusEligible {
		success(result.Reason)
	} else {
		fail(fmt.Sprintf("%s: %s", result.Status, result.Reason))
	}
	for _, warning := range result.Warnings {
		warn(warning)
	}

	fmt.Println()
	return nil
}
