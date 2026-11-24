package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

var (
	recoveryDryRun                  bool
	recoveryAcknowledgeRollbackLoss bool
	recoveryIncludeMetadata         bool
)

var cleanupRecoveryCmd = &cobra.Command{
	Use:   "cleanup-recovery <ce-name>",
	Short: "Remove rollback annotations after a protected OLMv1 adoption",
	Long: `Removes recovery annotations from an installed ClusterExtension after its
ClusterObjectDeployment has completed protected adoption. This permanently
disables rollback from the ClusterExtension's Subscription backup. The migration
provenance marker is retained. COD templates and COS revisions are not edited.

Use --dry-run to inspect the exact annotations that would be removed. The
--include-migration-metadata option also removes the ClusterExtension's migration
invocation and acknowledgement annotations.

Example:
  migrate-operators-v0-to-v1 cleanup-recovery my-operator --dry-run
  migrate-operators-v0-to-v1 cleanup-recovery my-operator --acknowledge-rollback-loss`,
	Args: cobra.ExactArgs(1),
	RunE: runAfterArgumentValidation(validateRecoveryCleanupArguments, runRecoveryCleanup),
}

func init() {
	cleanupRecoveryCmd.Flags().BoolVar(&recoveryDryRun, "dry-run", false, "Preview selected annotations without changing the ClusterExtension")
	cleanupRecoveryCmd.Flags().BoolVar(&recoveryAcknowledgeRollbackLoss, "acknowledge-rollback-loss", false, "Confirm removal of the Subscription backup used for rollback")
	cleanupRecoveryCmd.Flags().BoolVar(&recoveryIncludeMetadata, "include-migration-metadata", false, "Also remove migration invocation and acknowledgement annotations from the ClusterExtension")
}

func validateRecoveryCleanupArguments(_ *cobra.Command, args []string) error {
	if args[0] == "" {
		return fmt.Errorf("specify a ClusterExtension name")
	}
	return nil
}

func runRecoveryCleanup(cmd *cobra.Command, args []string) error {
	c, restCfg, err := newClient()
	if err != nil {
		return err
	}
	m := migration.NewMigrator(c, restCfg)
	m.Progress = progressFuncFor("cleanup-recovery", args[0])
	startProgress()
	result, err := m.CleanupRecoveryAnnotations(cmd.Context(), migration.RecoveryCleanupOptions{
		ClusterExtensionName:     args[0],
		AcknowledgeRollbackLoss:  recoveryAcknowledgeRollbackLoss,
		IncludeMigrationMetadata: recoveryIncludeMetadata,
		DryRun:                   recoveryDryRun,
	})
	clearProgress()
	if err == nil {
		err = progressError()
	}
	if err != nil {
		return err
	}
	return selectedCommandOutput().recoveryResult(result)
}
