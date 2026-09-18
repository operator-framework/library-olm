package migration

import (
	"fmt"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
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

	// BackupDirectory, when non-empty, writes OLM objects to disk before deletions (R2.6).
	BackupDirectory string

	// Soft eligibility override flags (R3). Setting a flag records an
	// acknowledged-<flag>:"true" annotation on the CE for audit (R2.5).
	AcknowledgeWatchScopeChange     bool // C1
	AcknowledgeOperatorCondition    bool // C4
	AcknowledgeOLMv0APIAccess       bool // C5
	AcknowledgeScopedServiceAccount bool // C6
	AcknowledgeNotSteadyState       bool // C8

	// AcknowledgeInstalled is required for Rollback when the CE is Installed=True.
	AcknowledgeInstalled bool

	// DeleteOperatorGroup deletes the OperatorGroup when both this flag is set AND
	// no other Subscriptions remain in the namespace (R6).
	DeleteOperatorGroup bool

	// SystemNamespace is the namespace where COS ref Secrets are created (R2.4).
	// When empty, migration discovers the operator-controller Deployment namespace.
	// It is an override for unusual installations, not a production default.
	SystemNamespace string
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

	// SubscriptionConfig holds spec.config from the Subscription for mapping to CE (R4).
	// spec.config.selector is dropped (never honored in OLMv0; no CE equivalent).
	SubscriptionConfig *operatorsv1alpha1.SubscriptionConfig

	// Subscription spec JSON for the CE migration-subscription-backup annotation (R2.5).
	SubscriptionBackupJSON string
	// OperatorGroup spec JSON for the CE migration-operatorgroup-backup annotation (R2.5).
	OperatorGroupBackupJSON string
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

// Backup holds serialized copies of OLMv0 resources for recovery and auditing.
type Backup struct {
	Subscription          *operatorsv1alpha1.Subscription
	ClusterServiceVersion *operatorsv1alpha1.ClusterServiceVersion
	OperatorGroup         *operatorsv1.OperatorGroup
	InstallPlan           *operatorsv1alpha1.InstallPlan
}

// SaveToDisk writes backup files to dir, creating it if absent. Per R2.6,
// failures here are non-fatal — the CE annotation backup is the authoritative path.
func (b *Backup) SaveToDisk(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}
	subscription := b.Subscription.DeepCopy()
	subscription.TypeMeta = metav1.TypeMeta{APIVersion: "operators.coreos.com/v1alpha1", Kind: "Subscription"}
	if err := writeYAMLFile(filepath.Join(dir, "subscription.yaml"), subscription); err != nil {
		return fmt.Errorf("failed to write subscription.yaml: %w", err)
	}
	if b.OperatorGroup != nil {
		operatorGroup := b.OperatorGroup.DeepCopy()
		operatorGroup.TypeMeta = metav1.TypeMeta{APIVersion: "operators.coreos.com/v1", Kind: "OperatorGroup"}
		if err := writeYAMLFile(filepath.Join(dir, "operatorgroup.yaml"), operatorGroup); err != nil {
			return fmt.Errorf("failed to write operatorgroup.yaml: %w", err)
		}
	}
	csv := b.ClusterServiceVersion.DeepCopy()
	csv.TypeMeta = metav1.TypeMeta{APIVersion: "operators.coreos.com/v1alpha1", Kind: "ClusterServiceVersion"}
	if err := writeYAMLFile(filepath.Join(dir, "clusterserviceversion.yaml"), csv); err != nil {
		return fmt.Errorf("failed to write clusterserviceversion.yaml: %w", err)
	}
	if b.InstallPlan != nil {
		ipDir := filepath.Join(dir, "installplans")
		if err := os.MkdirAll(ipDir, 0o750); err != nil {
			return fmt.Errorf("failed to create installplans directory: %w", err)
		}
		installPlan := b.InstallPlan.DeepCopy()
		installPlan.TypeMeta = metav1.TypeMeta{APIVersion: "operators.coreos.com/v1alpha1", Kind: "InstallPlan"}
		if err := writeYAMLFile(filepath.Join(ipDir, b.InstallPlan.Name+".yaml"), installPlan); err != nil {
			return fmt.Errorf("failed to write installplan: %w", err)
		}
	}
	return nil
}

func writeYAMLFile(path string, obj interface{}) error {
	data, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
