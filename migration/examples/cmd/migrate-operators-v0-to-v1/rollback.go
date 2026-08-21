package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	rollbackAll                bool
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
	RunE: runRollback,
}

func init() {
	rollbackCmd.Flags().BoolVar(&rollbackAll, "all", false, "Rollback all migrated ClusterExtensions")
	rollbackCmd.Flags().BoolVar(&rollbackAcknowledgeInstalled, "acknowledge-installed", false, "Confirm rollback even when CE is Installed=True")
}

func runRollback(cmd *cobra.Command, args []string) error {
	if rollbackAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both a CE name and --all")
	}
	if !rollbackAll && len(args) == 0 {
		return fmt.Errorf("specify a ClusterExtension name or --all")
	}

	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	m.Progress = progressFunc
	ctx := cmd.Context()

	if rollbackAll {
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
			info("No migrated ClusterExtensions found.")
			return nil
		}

		fmt.Printf("\nRolling back %d migrated ClusterExtension(s)...\n", len(targets))
		var firstErr error
		for _, name := range targets {
			if err := m.RollbackClusterExtension(ctx, name, rollbackAcknowledgeInstalled); err != nil {
				fail(fmt.Sprintf("%s: %v", name, err))
				if firstErr == nil {
					firstErr = err
				}
			} else {
				success(fmt.Sprintf("%s rolled back", name))
			}
		}
		return firstErr
	}

	ceName := args[0]
	fmt.Printf("\n%s%s🔄 Rolling back ClusterExtension %s...%s\n", colorBold, colorCyan, ceName, colorReset)

	if err := m.RollbackClusterExtension(ctx, ceName, rollbackAcknowledgeInstalled); err != nil {
		fail(fmt.Sprintf("Rollback failed: %v", err))
		return err
	}

	success(fmt.Sprintf("ClusterExtension %s rolled back; Subscription restored", ceName))
	fmt.Println()
	return nil
}

// rollbackSingleCE is a helper used when we have the CE object in hand.
func rollbackSingleCE(ctx interface{}, c client.Client, ce *ocv1.ClusterExtension, acknowledgeInstalled bool) error {
	_ = c
	_ = ce
	_ = ctx
	_ = acknowledgeInstalled
	return nil
}
