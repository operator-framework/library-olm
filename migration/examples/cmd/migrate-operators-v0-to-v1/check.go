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
	RunE: runAfterArgumentValidation(validateCheckArguments, runCheck),
}

func init() {
	checkCmd.Flags().StringVarP(&checkSubscriptionNamespace, "namespace", "n", "", "Subscription namespace (required without --all)")
	checkCmd.Flags().BoolVar(&checkAll, "all", false, "Check all Subscriptions on the cluster")
}

func validateCheckArguments(_ *cobra.Command, args []string) error {
	if checkAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both an operator name and --all")
	}
	if !checkAll && len(args) == 0 {
		return fmt.Errorf("specify an operator name or --all")
	}
	if !checkAll && checkSubscriptionNamespace == "" {
		return fmt.Errorf("-n/--namespace is required")
	}
	return nil
}

func runCheck(cmd *cobra.Command, args []string) error { //nolint:nestif
	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	output := selectedCommandOutput()
	m.Progress = progressFuncFor("check", "")
	ctx := cmd.Context()

	if checkAll {
		output.scanStart("check")
		startProgress()
		results, err := m.ScanAll(ctx)
		clearProgress()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}
		return output.scanResults("check", results)
	}

	operatorName := args[0]

	target := checkSubscriptionNamespace + "/" + operatorName
	output.checkStart(target)

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
	return output.checkResult("check", target, *result)
}
