// migrate-catalogs-v0-to-v1 migrates OLMv0 CatalogSources to OLMv1 ClusterCatalogs.
// Run this before migrate-operators-v0-to-v1.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/catalogmigration"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ocv1.AddToScheme(scheme))
	utilruntime.Must(operatorsv1alpha1.AddToScheme(scheme))
}

var (
	kubeconfig                  string
	dryRun                      bool
	deleteCatalogSource         bool
	acknowledgePriorityOverflow bool
)

var rootCmd = &cobra.Command{
	Use:   "migrate-catalogs-v0-to-v1",
	Short: "Migrate OLMv0 CatalogSources to OLMv1 ClusterCatalogs",
	Long: `Scans all CatalogSources across all namespaces and creates
corresponding OLMv1 ClusterCatalogs.

Only grpc-type CatalogSources with a spec.image are migratable.
configmap, internal, and address-only CatalogSources are reported as not migratable.

Run this before migrate-operators-v0-to-v1.

Examples:
  migrate-catalogs-v0-to-v1
  migrate-catalogs-v0-to-v1 --dry-run
  migrate-catalogs-v0-to-v1 --delete-catalogsource`,
	RunE: runMigrateCatalogs,
}

func init() {
	rootCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be created without modifying the cluster")
	rootCmd.Flags().BoolVar(&deleteCatalogSource, "delete-catalogsource", false, "Delete source CatalogSource after migration (only when no Subscription references it)")
	rootCmd.Flags().BoolVar(&acknowledgePriorityOverflow, "acknowledge-priority-overflow", false, "Cap out-of-range priority at MaxInt32/MinInt32 and proceed")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newClient() (client.Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	)

	restConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get REST config: %w", err)
	}

	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}
	return c, nil
}

func runMigrateCatalogs(cmd *cobra.Command, _ []string) error {
	c, err := newClient()
	if err != nil {
		return err
	}

	cm := catalogmigration.NewCatalogMigrator(c)
	opts := catalogmigration.CatalogMigratorOptions{
		DryRun:                    dryRun,
		DeleteCatalogSource:       deleteCatalogSource,
		AcknowledgePriorityOverflow: acknowledgePriorityOverflow,
	}

	if dryRun {
		fmt.Printf("\n🔍 Dry run — no cluster changes will be made.\n\n")
	} else {
		fmt.Printf("\n🔄 Migrating CatalogSources to ClusterCatalogs...\n\n")
	}

	results, err := cm.MigrateCatalogs(cmd.Context(), opts)
	if err != nil {
		return fmt.Errorf("catalog migration failed: %w", err)
	}

	// Print results grouped by status
	var created, adopted, skipped, errored, dryResults []catalogmigration.CatalogMigrationResult
	for _, r := range results {
		switch r.Status {
		case "created":
			created = append(created, r)
		case "adopted":
			adopted = append(adopted, r)
		case "skipped":
			skipped = append(skipped, r)
		case "error":
			errored = append(errored, r)
		case "dry-run":
			dryResults = append(dryResults, r)
		}
	}

	if len(dryResults) > 0 {
		fmt.Printf("Would migrate:\n")
		for _, r := range dryResults {
			fmt.Printf("  %-40s → %s\n    %s\n",
				r.CatalogSourceNamespace+"/"+r.CatalogSourceName,
				r.ClusterCatalogName, r.Reason)
		}
	}

	if len(created) > 0 {
		fmt.Printf("\n✅ Created (%d):\n", len(created))
		for _, r := range created {
			fmt.Printf("  ✓ %s/%s → ClusterCatalog/%s\n",
				r.CatalogSourceNamespace, r.CatalogSourceName, r.ClusterCatalogName)
		}
	}

	if len(adopted) > 0 {
		fmt.Printf("\n✅ Adopted existing (%d):\n", len(adopted))
		for _, r := range adopted {
			fmt.Printf("  ✓ %s/%s → ClusterCatalog/%s\n",
				r.CatalogSourceNamespace, r.CatalogSourceName, r.ClusterCatalogName)
		}
	}

	if len(skipped) > 0 {
		fmt.Printf("\n⏭  Skipped (%d):\n", len(skipped))
		for _, r := range skipped {
			fmt.Printf("  - %s/%s: %s\n",
				r.CatalogSourceNamespace, r.CatalogSourceName, r.Reason)
		}
	}

	if len(errored) > 0 {
		fmt.Printf("\n❌ Errors (%d):\n", len(errored))
		for _, r := range errored {
			fmt.Printf("  ✗ %s/%s: %s\n",
				r.CatalogSourceNamespace, r.CatalogSourceName, r.Reason)
		}
		return fmt.Errorf("%d catalog source(s) failed to migrate", len(errored))
	}

	total := len(created) + len(adopted)
	if total > 0 {
		fmt.Printf("\n✅ Done: %d ClusterCatalog(s) ready\n\n", total)
	} else if dryRun {
		fmt.Printf("\nDry run complete.\n\n")
	} else {
		fmt.Printf("\nNo migratable CatalogSources found.\n\n")
	}

	return nil
}
