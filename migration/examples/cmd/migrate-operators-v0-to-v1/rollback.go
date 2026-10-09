package main

import (
	"fmt"

	"github.com/spf13/cobra"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

var (
	rollbackAll                  bool
	rollbackAcknowledgeInstalled bool
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback [ce-name]",
	Short: "Restore an operator to OLMv0 management",
	Long: `Deletes the ClusterExtension and ClusterObjectSet (orphan cascade),
then restores the Subscription from the backup annotation.

Requires --acknowledge-installed when the ClusterExtension is Installed=True.

Target is a ClusterExtension name, or --all to rollback all migrated CEs.

Examples:
  migrate-operators-v0-to-v1 rollback my-operator --acknowledge-installed
  migrate-operators-v0-to-v1 rollback --all --acknowledge-installed`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAfterArgumentValidation(validateRollbackArguments, runRollback),
}

func init() {
	rollbackCmd.Flags().BoolVar(&rollbackAll, "all", false, "Rollback all migrated ClusterExtensions")
	rollbackCmd.Flags().BoolVar(&rollbackAcknowledgeInstalled, "acknowledge-installed", false, "Confirm rollback even when CE is Installed=True")
}

func validateRollbackArguments(_ *cobra.Command, args []string) error {
	if rollbackAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both a CE name and --all")
	}
	if !rollbackAll && len(args) == 0 {
		return fmt.Errorf("specify a ClusterExtension name or --all")
	}
	return nil
}

func runRollback(cmd *cobra.Command, args []string) error { //nolint:nestif
	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	output := selectedCommandOutput()
	m.Progress = progressFuncFor("rollback", "")
	ctx := cmd.Context()

	if rollbackAll { //nolint:nestif
		var ceList ocv1.ClusterExtensionList
		if err := c.List(ctx, &ceList); err != nil {
			return fmt.Errorf("failed to list ClusterExtensions: %w", err)
		}

		var targets []string
		for _, ce := range ceList.Items {
			if _, ok := ce.Annotations[migration.MigratedFromSubscriptionAnnotation]; ok {
				targets = append(targets, ce.Name)
			}
		}

		if len(targets) == 0 {
			return output.noTargets("rollback", "No migrated ClusterExtensions found")
		}

		output.batchStart("rollback", len(targets))
		var firstErr error
		for _, name := range targets {
			m.Progress = progressFuncFor("rollback", name)
			err := m.Rollback(ctx, migration.Options{ClusterExtensionName: name, AcknowledgeInstalled: rollbackAcknowledgeInstalled})
			if writeErr := output.batchResult("rollback", name, err); writeErr != nil {
				return writeErr
			}
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		return firstErr
	}

	ceName := args[0]
	m.Progress = progressFuncFor("rollback", ceName)
	output.singleStart("rollback", ceName)

	err = m.Rollback(ctx, migration.Options{ClusterExtensionName: ceName, AcknowledgeInstalled: rollbackAcknowledgeInstalled})
	if outputErr := output.singleResult("rollback", ceName, err); outputErr != nil {
		return outputErr
	}
	if err != nil {
		return err
	}
	return nil
}
