package migration

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
)

// OperatorStatus is the four-state classification of a Subscription's migration readiness.
type OperatorStatus string

const (
	OperatorStatusEligible        OperatorStatus = "Eligible"
	OperatorStatusIneligible      OperatorStatus = "Ineligible"
	OperatorStatusAlreadyMigrated OperatorStatus = "AlreadyMigrated"
	OperatorStatusConflict        OperatorStatus = "Conflict"
)

// Options configures the migration process.
type Options struct {
	SubscriptionName      string
	SubscriptionNamespace string
	ClusterExtensionName  string
	InstallNamespace      string
}

// ApplyDefaults fills in default values for any unset optional fields.
func (o *Options) ApplyDefaults() {
	if o.ClusterExtensionName == "" {
		o.ClusterExtensionName = o.SubscriptionName
	}
	if o.InstallNamespace == "" {
		o.InstallNamespace = o.SubscriptionNamespace
	}
}

// MigrationInfo holds the profiled operator information gathered during the migration.
type MigrationInfo struct {
	PackageName         string
	Version             string
	BundleName          string
	BundleImage         string
	Channel             string
	ManualApproval      bool // true if the Subscription had Manual install plan approval
	CatalogSourceRef    types.NamespacedName
	CatalogSourceImage  string // tag-based image from CatalogSource.Spec.Image
	ResolvedCatalogName string
	CollectedObjects    []unstructured.Unstructured
}

// ProgressFunc is called periodically during wait operations to report status.
type ProgressFunc func(message string)

// Migrator performs the migration operations using a controller-runtime client.
type Migrator struct {
	Client     client.Client
	RESTConfig *rest.Config
	Progress   ProgressFunc
}

// NewMigrator creates a new Migrator with the given client and REST config.
func NewMigrator(c client.Client, cfg *rest.Config) *Migrator {
	return &Migrator{Client: c, RESTConfig: cfg}
}

func (m *Migrator) progress(msg string) {
	if m.Progress != nil {
		m.Progress(msg)
	}
}

// Backup holds serialized copies of resources for recovery.
type Backup struct {
	Subscription          *operatorsv1alpha1.Subscription
	ClusterServiceVersion *operatorsv1alpha1.ClusterServiceVersion
}
