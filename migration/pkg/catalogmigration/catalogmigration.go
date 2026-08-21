// Package catalogmigration provides an API for migrating OLMv0 CatalogSources
// to OLMv1 ClusterCatalogs.
package catalogmigration

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

const (
	// MigratedFromCatalogSourceAnnotation is set on ClusterCatalog when first created or adopted.
	MigratedFromCatalogSourceAnnotation = "olm.operatorframework.io/migrated-from-catalogsource"

	defaultPollMinutes = 15
)

// CatalogMigratorOptions configures the catalog migration.
type CatalogMigratorOptions struct {
	DryRun                      bool
	DeleteCatalogSource         bool
	AcknowledgePriorityOverflow bool
}

// CatalogMigrationResult describes the outcome for a single CatalogSource.
type CatalogMigrationResult struct {
	CatalogSourceName      string
	CatalogSourceNamespace string
	ClusterCatalogName     string
	Status                 string // "created", "adopted", "skipped", "error", "dry-run"
	Reason                 string
}

// CatalogMigrator migrates OLMv0 CatalogSources to OLMv1 ClusterCatalogs.
type CatalogMigrator struct {
	Client client.Client
}

// NewCatalogMigrator creates a new CatalogMigrator.
func NewCatalogMigrator(c client.Client) *CatalogMigrator {
	return &CatalogMigrator{Client: c}
}

// MigrateCatalogs processes all CatalogSources across all namespaces and maps them to ClusterCatalogs.
// Strategy (per R8):
//   - Same name + same image across namespaces → consolidate into a single ClusterCatalog
//   - Same name + different image across namespaces → use <name>-<namespace> for each
//   - Unique name → use metadata.name directly
func (cm *CatalogMigrator) MigrateCatalogs(ctx context.Context, opts CatalogMigratorOptions) ([]CatalogMigrationResult, error) {
	// List all CatalogSources across all namespaces
	var csList operatorsv1alpha1.CatalogSourceList
	if err := cm.Client.List(ctx, &csList); err != nil {
		return nil, fmt.Errorf("failed to list CatalogSources: %w", err)
	}

	// List all existing ClusterCatalogs
	var ccList ocv1.ClusterCatalogList
	if err := cm.Client.List(ctx, &ccList); err != nil {
		return nil, fmt.Errorf("failed to list ClusterCatalogs: %w", err)
	}

	// Build map of existing ClusterCatalogs by image ref
	existingByImage := make(map[string]*ocv1.ClusterCatalog)
	for i := range ccList.Items {
		cc := &ccList.Items[i]
		if cc.Spec.Source.Image != nil && cc.Spec.Source.Image.Ref != "" {
			existingByImage[cc.Spec.Source.Image.Ref] = cc
		}
	}

	// List all Subscriptions to detect which CatalogSources are still referenced
	var subList operatorsv1alpha1.SubscriptionList
	if err := cm.Client.List(ctx, &subList); err != nil {
		return nil, fmt.Errorf("failed to list Subscriptions: %w", err)
	}

	// Build set of referenced CatalogSources
	referencedCS := make(map[string]bool)
	for _, sub := range subList.Items {
		key := fmt.Sprintf("%s/%s", sub.Spec.CatalogSourceNamespace, sub.Spec.CatalogSource)
		referencedCS[key] = true
	}

	// Determine naming strategy: group by name, check for image conflicts
	type csEntry struct {
		cs    operatorsv1alpha1.CatalogSource
		image string
	}
	byName := make(map[string][]csEntry)
	for _, cs := range csList.Items {
		if cs.Spec.SourceType != operatorsv1alpha1.SourceTypeGrpc || cs.Spec.Image == "" {
			continue // non-image sources handled separately below
		}
		byName[cs.Name] = append(byName[cs.Name], csEntry{cs: cs, image: cs.Spec.Image})
	}

	// For each name, determine if all entries share the same image
	nameStrategy := make(map[string]string) // cs name → "shared" or "namespace"
	for name, entries := range byName {
		allSame := true
		firstImage := entries[0].image
		for _, e := range entries[1:] {
			if e.image != firstImage {
				allSame = false
				break
			}
		}
		if allSame {
			nameStrategy[name] = "shared"
		} else {
			nameStrategy[name] = "namespace"
		}
	}

	var results []CatalogMigrationResult

	// Process non-image CatalogSources
	for _, cs := range csList.Items {
		if cs.Spec.SourceType == operatorsv1alpha1.SourceTypeGrpc && cs.Spec.Image != "" {
			continue // handled in the main loop below
		}
		reason := ""
		switch {
		case cs.Spec.SourceType == operatorsv1alpha1.SourceTypeConfigmap:
			reason = "configmap-type CatalogSource has no OLMv1 equivalent"
		case cs.Spec.SourceType == operatorsv1alpha1.SourceTypeInternal:
			reason = "internal-type CatalogSource has no OLMv1 equivalent"
		case cs.Spec.SourceType == operatorsv1alpha1.SourceTypeGrpc && cs.Spec.Image == "":
			reason = "grpc address-only CatalogSource (no spec.image) has no OLMv1 equivalent"
		default:
			reason = fmt.Sprintf("unsupported sourceType %q", cs.Spec.SourceType)
		}
		results = append(results, CatalogMigrationResult{
			CatalogSourceName:      cs.Name,
			CatalogSourceNamespace: cs.Namespace,
			Status:                 "skipped",
			Reason:                 reason,
		})
	}

	// Track which ClusterCatalog names we've already created this run (for consolidation)
	createdThisRun := make(map[string]bool)

	// Process image-type CatalogSources
	for _, cs := range csList.Items {
		if cs.Spec.SourceType != operatorsv1alpha1.SourceTypeGrpc || cs.Spec.Image == "" {
			continue
		}

		// Determine ClusterCatalog name
		var ccName string
		switch nameStrategy[cs.Name] {
		case "shared":
			ccName = cs.Name
		default:
			ccName = fmt.Sprintf("%s-%s", cs.Name, cs.Namespace)
		}

		// Validate and convert priority
		priority, priorityErr := validatePriority(cs.Spec.Priority, opts.AcknowledgePriorityOverflow)
		if priorityErr != nil {
			results = append(results, CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "skipped",
				Reason:                 priorityErr.Error(),
			})
			continue
		}

		// Convert poll interval
		pollMinutes := convertPollInterval(cs)

		csRef := fmt.Sprintf("%s/%s", cs.Namespace, cs.Name)

		// Check if already created this run (consolidation case)
		if createdThisRun[ccName] {
			results = append(results, CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "adopted",
				Reason:                 fmt.Sprintf("consolidated into shared ClusterCatalog %s", ccName),
			})
			continue
		}

		// Check if an existing ClusterCatalog matches by image
		if existing, found := existingByImage[cs.Spec.Image]; found {
			// Adopt: set annotation if not already present
			if opts.DryRun {
				results = append(results, CatalogMigrationResult{
					CatalogSourceName:      cs.Name,
					CatalogSourceNamespace: cs.Namespace,
					ClusterCatalogName:     existing.Name,
					Status:                 "dry-run",
					Reason:                 fmt.Sprintf("would adopt existing ClusterCatalog %s", existing.Name),
				})
				continue
			}

			if err := cm.annotateIfNotPresent(ctx, existing, csRef); err != nil {
				results = append(results, CatalogMigrationResult{
					CatalogSourceName:      cs.Name,
					CatalogSourceNamespace: cs.Namespace,
					ClusterCatalogName:     existing.Name,
					Status:                 "error",
					Reason:                 fmt.Sprintf("failed to annotate existing ClusterCatalog: %v", err),
				})
				continue
			}

			createdThisRun[existing.Name] = true
			results = append(results, CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     existing.Name,
				Status:                 "adopted",
				Reason:                 "existing ClusterCatalog with matching image adopted",
			})

			// Handle --delete-catalogsource
			if opts.DeleteCatalogSource && !referencedCS[csRef] {
				_ = cm.Client.Delete(ctx, &cs)
			}
			continue
		}

		// Create new ClusterCatalog
		if opts.DryRun {
			results = append(results, CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "dry-run",
				Reason:                 fmt.Sprintf("would create ClusterCatalog %s from image %s", ccName, cs.Spec.Image),
			})
			continue
		}

		imageSource := &ocv1.ImageSource{Ref: cs.Spec.Image}
		if pollMinutes > 0 {
			imageSource.PollIntervalMinutes = &pollMinutes
		}

		cc := &ocv1.ClusterCatalog{
			ObjectMeta: metav1.ObjectMeta{
				Name: ccName,
				Annotations: map[string]string{
					MigratedFromCatalogSourceAnnotation: csRef,
				},
			},
			Spec: ocv1.ClusterCatalogSpec{
				Source: ocv1.CatalogSource{
					Type:  ocv1.SourceTypeImage,
					Image: imageSource,
				},
				Priority:         priority,
				AvailabilityMode: ocv1.AvailabilityModeAvailable,
			},
		}

		if err := cm.Client.Create(ctx, cc); err != nil {
			results = append(results, CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "error",
				Reason:                 fmt.Sprintf("failed to create ClusterCatalog: %v", err),
			})
			continue
		}

		// Wait for serving
		if err := cm.waitForServing(ctx, ccName); err != nil {
			results = append(results, CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "error",
				Reason:                 fmt.Sprintf("ClusterCatalog not serving: %v", err),
			})
			continue
		}

		createdThisRun[ccName] = true
		existingByImage[cs.Spec.Image] = cc

		results = append(results, CatalogMigrationResult{
			CatalogSourceName:      cs.Name,
			CatalogSourceNamespace: cs.Namespace,
			ClusterCatalogName:     ccName,
			Status:                 "created",
			Reason:                 fmt.Sprintf("created from image %s", cs.Spec.Image),
		})

		// Handle --delete-catalogsource
		if opts.DeleteCatalogSource && !referencedCS[csRef] {
			_ = cm.Client.Delete(ctx, &cs)
		}
	}

	return results, nil
}

// annotateIfNotPresent sets MigratedFromCatalogSourceAnnotation on the ClusterCatalog
// if it is not already present (idempotent).
func (cm *CatalogMigrator) annotateIfNotPresent(ctx context.Context, cc *ocv1.ClusterCatalog, csRef string) error {
	if _, ok := cc.Annotations[MigratedFromCatalogSourceAnnotation]; ok {
		return nil // already set, leave it unchanged
	}

	patch := client.MergeFrom(cc.DeepCopy())
	if cc.Annotations == nil {
		cc.Annotations = make(map[string]string)
	}
	cc.Annotations[MigratedFromCatalogSourceAnnotation] = csRef
	return cm.Client.Patch(ctx, cc, patch)
}

// waitForServing polls until the ClusterCatalog has Serving=True.
func (cm *CatalogMigrator) waitForServing(ctx context.Context, ccName string) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		var cc ocv1.ClusterCatalog
		if err := cm.Client.Get(ctx, client.ObjectKey{Name: ccName}, &cc); err != nil {
			return false, err
		}
		for _, c := range cc.Status.Conditions {
			if c.Type == "Serving" && c.Status == metav1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

// validatePriority checks that the CatalogSource priority fits in int32 range.
// If it doesn't fit and AcknowledgePriorityOverflow is true, caps at MaxInt32/MinInt32.
func validatePriority(priority int, acknowledge bool) (int32, error) {
	if priority > math.MaxInt32 || priority < math.MinInt32 {
		if !acknowledge {
			return 0, fmt.Errorf("spec.priority %d is out of int32 range; pass --acknowledge-priority-overflow to cap and proceed", priority)
		}
		if priority > math.MaxInt32 {
			return math.MaxInt32, nil
		}
		return math.MinInt32, nil
	}
	return int32(priority), nil //nolint:gosec // validated above
}

// convertPollInterval converts the CatalogSource registryPoll interval to integer minutes.
// Returns 0 if no interval is set, or if the image ref is digest-based (poll not allowed).
func convertPollInterval(cs operatorsv1alpha1.CatalogSource) int {
	// Digest-based refs must not have a poll interval
	if strings.Contains(cs.Spec.Image, "@sha256:") {
		return 0
	}

	if cs.Spec.UpdateStrategy == nil || cs.Spec.UpdateStrategy.RegistryPoll == nil {
		return defaultPollMinutes
	}

	interval := cs.Spec.UpdateStrategy.Interval
	if interval == nil || interval.Duration == 0 {
		return 0
	}

	minutes := int(interval.Minutes())
	if minutes < 1 {
		minutes = 1
	}
	return minutes
}
