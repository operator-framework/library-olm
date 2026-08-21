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
	RunE: runCleanup,
}

func init() {
	cleanupCmd.Flags().BoolVar(&cleanupAll, "all", false, "Cleanup all Conflict-state ClusterExtensions")
}

func runCleanup(cmd *cobra.Command, args []string) error { //nolint:nestif
	if cleanupAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both a CE name and --all")
	}
	if !cleanupAll && len(args) == 0 {
		return fmt.Errorf("specify a ClusterExtension name or --all")
	}

	c, restCfg, err := newClient()
	if err != nil {
		return err
	}

	m := migration.NewMigrator(c, restCfg)
	m.Progress = progressFunc
	ctx := cmd.Context()

	if cleanupAll { //nolint:nestif
		// Find all CEs that are in Conflict state
		results, err := m.ScanAllSubscriptions(ctx)
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
			info("No Conflict-state ClusterExtensions found.")
			return nil
		}

		fmt.Printf("\nCleaning up %d Conflict-state ClusterExtension(s)...\n", len(conflictCEs))
		var firstErr error
		for _, ceName := range conflictCEs {
			if err := m.CleanupConflict(ctx, ceName); err != nil {
				fail(fmt.Sprintf("%s: %v", ceName, err))
				if firstErr == nil {
					firstErr = err
				}
			} else {
				success(fmt.Sprintf("%s conflict resolved", ceName))
			}
		}
		return firstErr
	}

	ceName := args[0]
	fmt.Printf("\n%s%s🧹 Cleaning up Conflict for %s...%s\n", colorBold, colorCyan, ceName, colorReset)

	if err := m.CleanupConflict(ctx, ceName); err != nil {
		fail(fmt.Sprintf("Cleanup failed: %v", err))
		return err
	}

	success(fmt.Sprintf("Conflict resolved for %s; OLMv0 artifacts removed, ClusterExtension intact", ceName))
	fmt.Println()
	return nil
}
