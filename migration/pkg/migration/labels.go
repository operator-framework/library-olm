package migration

// Label and annotation keys used by the migration tool.
// These match the values expected by operator-controller but are defined here
// to avoid importing internal packages from that module.
const (
	// LabelOwnerKind is set on ClusterObjectSet to indicate its owner's kind.
	LabelOwnerKind = "olm.operatorframework.io/owner-kind"
	// LabelOwnerName is set on ClusterObjectSet to indicate its owner's name.
	LabelOwnerName = "olm.operatorframework.io/owner-name"
	// LabelRevisionName is set on ref Secrets to identify the ClusterObjectSet.
	LabelRevisionName = "olm.operatorframework.io/revision-name"
	// LabelPackageName records the operator package associated with a ClusterObjectSet.
	LabelPackageName = "olm.operatorframework.io/package-name"
	// LabelBundleName records the bundle name for a ClusterObjectSet.
	LabelBundleName = "olm.operatorframework.io/bundle-name"
	// LabelBundleVersion records the bundle version for a ClusterObjectSet.
	LabelBundleVersion = "olm.operatorframework.io/bundle-version"
	// LabelBundleReference records the bundle image reference for a ClusterObjectSet.
	LabelBundleReference = "olm.operatorframework.io/bundle-reference"
	// LabelMetadataName is the well-known label key for ClusterCatalog name selection.
	LabelMetadataName = "olm.operatorframework.io/metadata.name"

	// SecretTypeObjectData is the Secret type for externalized COS object content.
	SecretTypeObjectData = "olm.operatorframework.io/object-data" //nolint:gosec // G101 false positive: this is a Kubernetes Secret type identifier, not a credential

	// MigratedFromSubscriptionAnnotation is set on both the COS and CE.
	// Value is "<namespace>/<name>" of the source Subscription.
	MigratedFromSubscriptionAnnotation = "olm.operatorframework.io/migrated-from-subscription"

	// MigratedFromCatalogSourceAnnotation is set on ClusterCatalog by the catalog migration tool.
	MigratedFromCatalogSourceAnnotation = "olm.operatorframework.io/migrated-from-catalogsource"

	// fieldManager is the SSA field manager used for all apply operations.
	fieldManager = "olm.operatorframework.io/migration"
)
