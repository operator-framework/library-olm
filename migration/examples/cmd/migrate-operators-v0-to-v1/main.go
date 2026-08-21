// migrate-operators-v0-to-v1 is a CLI tool for migrating operators managed by
// OLMv0 (Subscription/CSV) to OLMv1 (ClusterExtension/ClusterObjectSet).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ocv1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(operatorsv1.AddToScheme(scheme))
	utilruntime.Must(operatorsv1alpha1.AddToScheme(scheme))
}

var kubeconfig string

var rootCmd = &cobra.Command{
	Use:   "migrate-operators-v0-to-v1",
	Short: "Migrate OLMv0-managed operators to OLMv1",
	Long: `migrate-operators-v0-to-v1 migrates operators from OLMv0 (Subscription/CSV)
to OLMv1 (ClusterExtension/ClusterObjectSet).

Run migrate-catalogs-v0-to-v1 first to create ClusterCatalogs from CatalogSources.

Subcommands:
  check   — report readiness and compatibility (no changes)
  convert — perform the migration
  rollback — restore an operator to OLMv0 management
  cleanup — finish a partial migration (Conflict state)`,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: KUBECONFIG env or ~/.kube/config)")

	rootCmd.AddCommand(checkCmd)
	rootCmd.AddCommand(convertCmd)
	rootCmd.AddCommand(rollbackCmd)
	rootCmd.AddCommand(cleanupCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newClient() (client.Client, *rest.Config, error) {
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
		return nil, nil, fmt.Errorf("failed to get REST config: %w", err)
	}

	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create client: %w", err)
	}
	return c, restConfig, nil
}
