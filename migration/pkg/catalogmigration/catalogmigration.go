// Package catalogmigration maps image-backed OLMv0 CatalogSources to OLMv1
// ClusterCatalogs. It can create a catalog or adopt an existing catalog with
// the same image. Unsupported sources are reported as skipped rather than
// converted. MigrateCatalogs returns a result for each source, so callers must
// inspect per-source statuses as well as the returned error.
package catalogmigration

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

const (
	// MigratedFromCatalogSourceAnnotation is set on ClusterCatalog when first created or adopted.
	MigratedFromCatalogSourceAnnotation = "olm.operatorframework.io/migrated-from-catalogsource"
	clusterCatalogServingTimeout        = 10 * time.Minute
)

// CatalogMigratorOptions configures catalog migration. DryRun reports proposed
// changes without mutating catalogs. DeleteCatalogSource requests source
// deletion only when no Subscription references it. AcknowledgePriorityOverflow
// permits out-of-range priorities to be capped to int32 limits.
type CatalogMigratorOptions struct {
	DryRun                      bool
	DeleteCatalogSource         bool
	AcknowledgePriorityOverflow bool
}

// CatalogMigrationResult describes one CatalogSource outcome. Status is
// "created", "adopted", "skipped", "error", or "dry-run". Reason explains the
// outcome and Notes describe source settings without an OLMv1 equivalent.
type CatalogMigrationResult struct {
	CatalogSourceName      string
	CatalogSourceNamespace string
	ClusterCatalogName     string
	Status                 string // "created", "adopted", "skipped", "error", "dry-run"
	Reason                 string
	Notes                  []string // informational notices (e.g. dropped fields with no OLMv1 equivalent)
}

// CatalogMigrator migrates OLMv0 CatalogSources to OLMv1 ClusterCatalogs.
type CatalogMigrator struct {
	Client client.Client
	// Progress receives the same typed, synchronous events as migration.Migrator.
	// Per-source outcomes remain available in MigrateCatalogs results.
	Progress migration.ProgressFunc
}

// NewCatalogMigrator creates a new CatalogMigrator.
func NewCatalogMigrator(c client.Client) *CatalogMigrator {
	return &CatalogMigrator{Client: c}
}

func (cm *CatalogMigrator) progress(event migration.ProgressEvent) {
	if cm.Progress != nil {
		cm.Progress(event)
	}
}

func (cm *CatalogMigrator) listFailure(kind string, err error) error {
	wrapped := fmt.Errorf("failed to list %s: %w", kind, err)
	cm.progress(migration.ProgressEvent{Step: migration.ProgressStepScan, Status: migration.ProgressFailed, Message: wrapped.Error(), Err: wrapped})
	return wrapped
}

// MigrateCatalogs processes CatalogSources in all namespaces. Only grpc sources
// with spec.image can become ClusterCatalogs; unsupported sources are returned
// as skipped results. Existing catalogs with matching images are adopted, and
// Sources with the same name share a catalog only when all have the same image.
// If any image differs, all sources in that group are namespace-qualified.
//
// The returned slice contains one outcome per source. Per-source create,
// annotation, serving, or deletion-preflight failures appear as results with
// Status "error" rather than as the returned error; callers must inspect every
// result. The returned error covers failures to list the source catalogs. DryRun reports
// proposed actions without creating or annotating catalogs.
func (cm *CatalogMigrator) MigrateCatalogs(ctx context.Context, opts CatalogMigratorOptions) ([]CatalogMigrationResult, error) {
	cm.progress(migration.ProgressEvent{Step: migration.ProgressStepScan, Status: migration.ProgressStarted, Message: "Scanning CatalogSources and migration references"})
	// List all CatalogSources across all namespaces
	var csList operatorsv1alpha1.CatalogSourceList
	if err := cm.Client.List(ctx, &csList); err != nil {
		return nil, cm.listFailure("CatalogSources", err)
	}

	// List all existing ClusterCatalogs
	var ccList ocv1.ClusterCatalogList
	if err := cm.Client.List(ctx, &ccList); err != nil {
		return nil, cm.listFailure("ClusterCatalogs", err)
	}

	// Keep all existing catalogs for each image. More than one ClusterCatalog
	// can technically reference an image; resolveExistingClusterCatalog chooses
	// one deterministically rather than depending on list order.
	existingByImage := make(map[string][]*ocv1.ClusterCatalog)
	existingByName := make(map[string]*ocv1.ClusterCatalog)
	for i := range ccList.Items {
		cc := &ccList.Items[i]
		existingByName[cc.Name] = cc
		if cc.Spec.Source.Image != nil && cc.Spec.Source.Image.Ref != "" {
			existingByImage[cc.Spec.Source.Image.Ref] = append(existingByImage[cc.Spec.Source.Image.Ref], cc)
		}
	}
	for _, catalogs := range existingByImage {
		sort.Slice(catalogs, func(i, j int) bool { return catalogs[i].Name < catalogs[j].Name })
	}
	sort.Slice(csList.Items, func(i, j int) bool {
		if csList.Items[i].Namespace == csList.Items[j].Namespace {
			return csList.Items[i].Name < csList.Items[j].Name
		}
		return csList.Items[i].Namespace < csList.Items[j].Namespace
	})

	cm.progress(migration.ProgressEvent{Step: migration.ProgressStepScan, Status: migration.ProgressCompleted, Message: fmt.Sprintf("Found %d CatalogSource(s)", len(csList.Items))})
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
	recordResult := func(result CatalogMigrationResult, cause error) {
		results = append(results, result)
		target := result.CatalogSourceNamespace + "/" + result.CatalogSourceName
		status := migration.ProgressCompleted
		switch result.Status {
		case "skipped":
			status = migration.ProgressWarning
		case "error":
			status = migration.ProgressFailed
		}
		cm.progress(migration.ProgressEvent{
			Step: migration.ProgressStepCatalog, Status: status,
			Target: target, Message: fmt.Sprintf("CatalogSource %s: %s", target, result.Reason), Err: cause,
		})
		for _, note := range result.Notes {
			cm.progress(migration.ProgressEvent{Step: migration.ProgressStepCatalog, Status: migration.ProgressNote, Target: target, Message: note})
		}
	}

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
		recordResult(CatalogMigrationResult{
			CatalogSourceName:      cs.Name,
			CatalogSourceNamespace: cs.Namespace,
			Status:                 "skipped",
			Reason:                 reason,
		}, nil)
	}

	// Track which ClusterCatalog names we've already created this run (for consolidation)
	createdThisRun := make(map[string]string) // ClusterCatalog name → source image

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
			recordResult(CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "skipped",
				Reason:                 priorityErr.Error(),
			}, nil)
			continue
		}

		// Convert poll interval
		pollMinutes := convertPollInterval(cs)

		// Collect informational notes for fields that have no OLMv1 equivalent (R8).
		var notes []string
		if len(cs.Spec.Secrets) > 0 {
			notes = append(notes, fmt.Sprintf("spec.secrets (%d secret(s)) has no OLMv1 equivalent; OLMv1 uses the cluster global pull secret", len(cs.Spec.Secrets)))
		}
		if cs.Spec.GrpcPodConfig != nil {
			notes = append(notes, "spec.grpcPodConfig has no OLMv1 equivalent; catalogd manages the serving pod configuration")
		}

		csRef := fmt.Sprintf("%s/%s", cs.Namespace, cs.Name)

		// Check if already created this run (consolidation case)
		if image, ok := createdThisRun[ccName]; ok {
			if image != cs.Spec.Image {
				collisionErr := fmt.Errorf("ClusterCatalog %s already selected with image %q, which does not match CatalogSource image %q", ccName, image, cs.Spec.Image)
				recordResult(CatalogMigrationResult{
					CatalogSourceName:      cs.Name,
					CatalogSourceNamespace: cs.Namespace,
					ClusterCatalogName:     ccName,
					Status:                 "error",
					Reason:                 collisionErr.Error(),
					Notes:                  notes,
				}, collisionErr)
				continue
			}
			result := CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "adopted",
				Reason:                 fmt.Sprintf("consolidated into shared ClusterCatalog %s", ccName),
				Notes:                  notes,
			}
			cm.handleCatalogSourceDeletion(ctx, &cs, csRef, opts, &result)
			recordResult(result, nil)
			continue
		}

		// Check if an existing ClusterCatalog matches by image
		if existing := resolveExistingClusterCatalog(existingByImage[cs.Spec.Image], ccName); existing != nil {
			// Adopt: set annotation if not already present
			if opts.DryRun {
				result := CatalogMigrationResult{
					CatalogSourceName:      cs.Name,
					CatalogSourceNamespace: cs.Namespace,
					ClusterCatalogName:     existing.Name,
					Status:                 "dry-run",
					Reason:                 fmt.Sprintf("would adopt existing ClusterCatalog %s", existing.Name),
					Notes:                  notes,
				}
				cm.handleCatalogSourceDeletion(ctx, &cs, csRef, opts, &result)
				recordResult(result, nil)
				continue
			}

			if err := cm.annotateIfNotPresent(ctx, existing, csRef); err != nil {
				recordResult(CatalogMigrationResult{
					CatalogSourceName:      cs.Name,
					CatalogSourceNamespace: cs.Namespace,
					ClusterCatalogName:     existing.Name,
					Status:                 "error",
					Reason:                 fmt.Sprintf("failed to annotate existing ClusterCatalog: %v", err),
					Notes:                  notes,
				}, fmt.Errorf("failed to annotate existing ClusterCatalog: %w", err))
				continue
			}

			createdThisRun[existing.Name] = cs.Spec.Image
			result := CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     existing.Name,
				Status:                 "adopted",
				Reason:                 "existing ClusterCatalog with matching image adopted",
				Notes:                  notes,
			}
			cm.handleCatalogSourceDeletion(ctx, &cs, csRef, opts, &result)
			recordResult(result, nil)
			continue
		}

		// A different image already owns the name this source would use. Do not
		// rely on Create returning AlreadyExists: report a safe, actionable
		// per-source error and leave both resources unchanged.
		if existing := existingByName[ccName]; existing != nil {
			existingImage := ""
			if existing.Spec.Source.Image != nil {
				existingImage = existing.Spec.Source.Image.Ref
			}
			collisionErr := fmt.Errorf("ClusterCatalog %s already exists with image %q, which does not match CatalogSource image %q", ccName, existingImage, cs.Spec.Image)
			recordResult(CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "error",
				Reason:                 collisionErr.Error(),
				Notes:                  notes,
			}, collisionErr)
			continue
		}

		// Create new ClusterCatalog
		if opts.DryRun {
			result := CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "dry-run",
				Reason:                 fmt.Sprintf("would create ClusterCatalog %s from image %s", ccName, cs.Spec.Image),
				Notes:                  notes,
			}
			cm.handleCatalogSourceDeletion(ctx, &cs, csRef, opts, &result)
			recordResult(result, nil)
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
			recordResult(CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "error",
				Reason:                 fmt.Sprintf("failed to create ClusterCatalog: %v", err),
				Notes:                  notes,
			}, fmt.Errorf("failed to create ClusterCatalog: %w", err))
			continue
		}

		// Wait for serving
		cm.progress(migration.ProgressEvent{Step: migration.ProgressStepCatalog, Status: migration.ProgressWaiting, Target: csRef, Message: fmt.Sprintf("Waiting for ClusterCatalog %s to become Serving=True", ccName)})
		if err := cm.waitForServing(ctx, ccName); err != nil {
			recordResult(CatalogMigrationResult{
				CatalogSourceName:      cs.Name,
				CatalogSourceNamespace: cs.Namespace,
				ClusterCatalogName:     ccName,
				Status:                 "error",
				Reason:                 fmt.Sprintf("ClusterCatalog not serving: %v", err),
				Notes:                  notes,
			}, fmt.Errorf("ClusterCatalog not serving: %w", err))
			continue
		}

		createdThisRun[ccName] = cs.Spec.Image
		existingByImage[cs.Spec.Image] = append(existingByImage[cs.Spec.Image], cc)
		existingByName[cc.Name] = cc

		result := CatalogMigrationResult{
			CatalogSourceName:      cs.Name,
			CatalogSourceNamespace: cs.Namespace,
			ClusterCatalogName:     ccName,
			Status:                 "created",
			Reason:                 fmt.Sprintf("created from image %s", cs.Spec.Image),
			Notes:                  notes,
		}
		cm.handleCatalogSourceDeletion(ctx, &cs, csRef, opts, &result)
		recordResult(result, nil)
	}

	return results, nil
}

// resolveExistingClusterCatalog returns a matching catalog. Prefer the name
// selected for this CatalogSource, then use the lexicographically first match
// so a duplicate image reference cannot make migration nondeterministic.
func resolveExistingClusterCatalog(catalogs []*ocv1.ClusterCatalog, preferredName string) *ocv1.ClusterCatalog {
	var fallback *ocv1.ClusterCatalog
	for _, catalog := range catalogs {
		if catalog.Name == preferredName {
			return catalog
		}
		if fallback == nil || catalog.Name < fallback.Name {
			fallback = catalog
		}
	}
	return fallback
}

// handleCatalogSourceDeletion implements the deliberately conservative source
// cleanup policy. It records both a proposed dry-run deletion and failures so
// callers never mistake a migrated catalog for successful source cleanup.
func (cm *CatalogMigrator) handleCatalogSourceDeletion(ctx context.Context, cs *operatorsv1alpha1.CatalogSource, csRef string, opts CatalogMigratorOptions, result *CatalogMigrationResult) {
	if !opts.DeleteCatalogSource {
		return
	}
	var subscriptions operatorsv1alpha1.SubscriptionList
	if err := cm.Client.List(ctx, &subscriptions); err != nil {
		result.Status = "error"
		result.Reason = fmt.Sprintf("%s; failed to list Subscriptions before CatalogSource deletion: %v", result.Reason, err)
		return
	}
	for _, sub := range subscriptions.Items {
		if sub.Spec != nil && sub.Spec.CatalogSourceNamespace+"/"+sub.Spec.CatalogSource == csRef {
			result.Notes = append(result.Notes, "CatalogSource retained because one or more Subscriptions still reference it")
			return
		}
	}
	if opts.DryRun {
		result.Notes = append(result.Notes, "would delete unreferenced CatalogSource")
		return
	}
	if err := cm.Client.Delete(ctx, cs); err != nil && !apierrors.IsNotFound(err) {
		result.Status = "error"
		result.Reason = fmt.Sprintf("%s; failed to delete unreferenced CatalogSource: %v", result.Reason, err)
		return
	}
	result.Notes = append(result.Notes, "deleted unreferenced CatalogSource")
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
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, clusterCatalogServingTimeout, true, func(ctx context.Context) (bool, error) {
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

	// Only convert explicitly set values (R8). When no UpdateStrategy is configured,
	// leave pollIntervalMinutes unset so OLMv1 uses its own default rather than
	// inheriting the OLMv0 default (15m) which was never explicitly chosen.
	if cs.Spec.UpdateStrategy == nil || cs.Spec.UpdateStrategy.RegistryPoll == nil {
		return 0
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
