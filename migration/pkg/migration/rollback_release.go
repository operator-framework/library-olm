package migration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

const (
	boxcutterRevisionAnnotation       = "olm.operatorframework.io/revision"
	boxcutterManagedByLabel           = "app.kubernetes.io/managed-by"
	boxcutterManagedLabel             = "boxcutter-managed"
	rollbackReleaseSnapshotAnnotation = "olm.operatorframework.io/rollback-release-snapshot"
)

type rollbackObject struct {
	Identity  unstructured.Unstructured `json:"identity"`
	UID       types.UID                 `json:"uid"`
	OwnerUID  types.UID                 `json:"ownerUID"`
	Revision  string                    `json:"revision"`
	ManagedBy string                    `json:"managedBy"`
}

// snapshotRevisionObjects records only resources currently controlled by a
// revision being removed. It runs before deletion, while ownership is provable.
func (m *Migrator) snapshotRevisionObjects(ctx context.Context, revisions []ocv1.ClusterObjectSet) ([]rollbackObject, error) {
	var objects []rollbackObject
	seen := map[string]bool{}
	for i := range revisions {
		revision := &revisions[i]
		for _, phase := range revision.Spec.Phases {
			for _, entry := range phase.Objects {
				manifest := entry.Object
				if entry.Ref.Name != "" {
					var err error
					manifest, err = m.rollbackReferencedObject(ctx, entry.Ref)
					if err != nil {
						return nil, fmt.Errorf("ClusterObjectSet %s phase %s: %w", revision.Name, phase.Name, err)
					}
				}
				if manifest.GetAPIVersion() == "" || manifest.GetKind() == "" || manifest.GetName() == "" {
					return nil, fmt.Errorf("ClusterObjectSet %s phase %s has an incomplete object identity", revision.Name, phase.Name)
				}
				key := resourceKey(manifest)
				if seen[key] {
					continue
				}
				live := &unstructured.Unstructured{}
				live.SetGroupVersionKind(manifest.GroupVersionKind())
				if err := m.Client.Get(ctx, client.ObjectKeyFromObject(&manifest), live); err != nil {
					if apierrors.IsNotFound(err) {
						continue
					}
					return nil, fmt.Errorf("getting %s for rollback: %w", key, err)
				}
				owner := metav1.GetControllerOf(live)
				if owner == nil || owner.UID != revision.UID || owner.Kind != "ClusterObjectSet" {
					continue
				}
				if live.GetAnnotations()[boxcutterRevisionAnnotation] == "" {
					continue
				}
				seen[key] = true
				identity := unstructured.Unstructured{}
				identity.SetGroupVersionKind(live.GroupVersionKind())
				identity.SetName(live.GetName())
				identity.SetNamespace(live.GetNamespace())
				objects = append(objects, rollbackObject{
					Identity: identity, UID: live.GetUID(), OwnerUID: owner.UID,
					Revision:  live.GetAnnotations()[boxcutterRevisionAnnotation],
					ManagedBy: live.GetLabels()[boxcutterManagedByLabel],
				})
			}
		}
	}
	return objects, nil
}

func (m *Migrator) rollbackReferencedObject(ctx context.Context, ref ocv1.ObjectSourceRef) (unstructured.Unstructured, error) {
	var secret corev1.Secret
	if err := m.Client.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		return unstructured.Unstructured{}, fmt.Errorf("getting object Secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	data, ok := secret.Data[ref.Key]
	if !ok {
		return unstructured.Unstructured{}, fmt.Errorf("object Secret %s/%s has no key %q", ref.Namespace, ref.Name, ref.Key)
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return unstructured.Unstructured{}, fmt.Errorf("opening compressed object: %w", err)
		}
		data, err = io.ReadAll(io.LimitReader(reader, 10*1024*1024+1))
		closeErr := reader.Close()
		if err != nil {
			return unstructured.Unstructured{}, fmt.Errorf("reading compressed object: %w", err)
		}
		if closeErr != nil {
			return unstructured.Unstructured{}, fmt.Errorf("closing compressed object: %w", closeErr)
		}
		if len(data) > 10*1024*1024 {
			return unstructured.Unstructured{}, fmt.Errorf("compressed object exceeds 10 MiB")
		}
	}
	var object unstructured.Unstructured
	if err := json.Unmarshal(data, &object.Object); err != nil {
		return unstructured.Unstructured{}, fmt.Errorf("decoding object Secret %s/%s key %q: %w", ref.Namespace, ref.Name, ref.Key, err)
	}
	return object, nil
}

// releaseRevisionObjects waits for orphan propagation, then removes metadata
// written by the old object controller. The UID and recorded metadata guard
// against modifying an object replaced or adopted during rollback.
func (m *Migrator) releaseRevisionObjects(ctx context.Context, revisions []ocv1.ClusterObjectSet, objects []rollbackObject) error {
	for i := range revisions {
		name := revisions[i].Name
		if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			var current ocv1.ClusterObjectSet
			err := m.Client.Get(ctx, client.ObjectKey{Name: name}, &current)
			return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
		}); err != nil {
			return fmt.Errorf("waiting for ClusterObjectSet %s deletion: %w", name, err)
		}
	}
	for _, original := range objects {
		key := client.ObjectKeyFromObject(&original.Identity)
		if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			current := &unstructured.Unstructured{}
			current.SetGroupVersionKind(original.Identity.GroupVersionKind())
			if err := m.Client.Get(ctx, key, current); err != nil {
				return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
			}
			if current.GetUID() != original.UID {
				return false, fmt.Errorf("resource %s was replaced during rollback", key)
			}
			if owner := metav1.GetControllerOf(current); owner != nil {
				if owner.UID != original.OwnerUID {
					return false, fmt.Errorf("resource %s acquired another controller during rollback", key)
				}
				return false, nil
			}
			currentRevision := current.GetAnnotations()[boxcutterRevisionAnnotation]
			currentManagedBy := current.GetLabels()[boxcutterManagedByLabel]
			if currentRevision != "" && currentRevision != original.Revision ||
				currentManagedBy != "" && currentManagedBy != original.ManagedBy {
				return false, fmt.Errorf("ownership metadata on %s changed during rollback", key)
			}
			if currentRevision == "" && currentManagedBy == "" && current.GetLabels()[boxcutterManagedLabel] == "" {
				return true, nil
			}
			updated := current.DeepCopy()
			annotations := updated.GetAnnotations()
			delete(annotations, boxcutterRevisionAnnotation)
			updated.SetAnnotations(annotations)
			labels := updated.GetLabels()
			if original.ManagedBy == "olm.operatorframework.io" {
				delete(labels, boxcutterManagedByLabel)
			}
			delete(labels, boxcutterManagedLabel)
			updated.SetLabels(labels)
			if err := m.Client.Patch(ctx, updated, client.MergeFromWithOptions(current, client.MergeFromWithOptimisticLock{})); err != nil {
				if apierrors.IsConflict(err) {
					return false, nil
				}
				return false, fmt.Errorf("releasing %s: %w", key, err)
			}
			return true, nil
		}); err != nil {
			return fmt.Errorf("waiting to release %s: %w", key, err)
		}
	}
	return nil
}
