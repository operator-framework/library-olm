package main

import (
	"fmt"

	"github.com/spf13/cobra"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

var cleanupAll bool

var cleanupCmd = &cobra.Command{
	Use:   "cleanup [ce-name]",
	Short: "Finish a partial migration (Conflict state)",
	Long: `Resolves a Conflict state by deleting the Subscription and OLMv0 artifacts,
leaving the ClusterExtension intact.

Use this when both a Subscription and an annotated ClusterExtension exist,
indicating a failed cleanup from a previous migration attempt.

Target is a ClusterExtension name, or --all to cleanup all conflicts.

Examples:
  migrate-operators-v0-to-v1 cleanup my-operator
  migrate-operators-v0-to-v1 cleanup --all`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAfterArgumentValidation(validateCleanupArguments, runCleanup),
}

func init() {
	cleanupCmd.Flags().BoolVar(&cleanupAll, "all", false, "Cleanup all Conflict-state ClusterExtensions")
}

func validateCleanupArguments(_ *cobra.Command, args []string) error {
	if cleanupAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both a CE name and --all")
	}
	if !cleanupAll && len(args) == 0 {
		return fmt.Errorf("specify a ClusterExtension name or --all")
	}
	return nil
}

func runCleanup(cmd *cobra.Command, args []string) error { //nolint:nestif
	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	output := selectedCommandOutput()
	m.Progress = progressFuncFor("cleanup", "")
	ctx := cmd.Context()

	if cleanupAll { //nolint:nestif
		// Find all CEs that are in Conflict state
		results, err := m.ScanAll(ctx)
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		// Also scan CEs for those with the annotation
		var ceList ocv1.ClusterExtensionList
		if err := c.List(ctx, &ceList); err != nil {
			return fmt.Errorf("failed to list ClusterExtensions: %w", err)
		}

		var conflictCEs []string
		for _, r := range results {
			if r.Status == migration.OperatorStatusConflict {
				// Find the CE name from the annotation
				subRef := fmt.Sprintf("%s/%s", r.SubscriptionNamespace, r.SubscriptionName)
				for _, ce := range ceList.Items {
					if ce.Annotations[migration.MigratedFromSubscriptionAnnotation] == subRef {
						conflictCEs = append(conflictCEs, ce.Name)
						break
					}
				}
			}
		}

		if len(conflictCEs) == 0 {
			return output.noTargets("cleanup", "No Conflict-state ClusterExtensions found")
		}

		output.batchStart("cleanup", len(conflictCEs))
		var firstErr error
		for _, ceName := range conflictCEs {
			m.Progress = progressFuncFor("cleanup", ceName)
			startProgress()
			err := m.Cleanup(ctx, migration.Options{ClusterExtensionName: ceName})
			clearProgress()
			if err == nil {
				err = progressError()
			}
			if writeErr := output.batchResult("cleanup", ceName, err); writeErr != nil {
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
	m.Progress = progressFuncFor("cleanup", ceName)
	output.singleStart("cleanup", ceName)

	startProgress()
	err = m.Cleanup(ctx, migration.Options{ClusterExtensionName: ceName})
	clearProgress()
	if err == nil {
		err = progressError()
	}
	if outputErr := output.singleResult("cleanup", ceName, err); outputErr != nil {
		return outputErr
	}
	if err != nil {
		return err
	}
	return nil
}
