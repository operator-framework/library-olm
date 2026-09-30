//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

type rollbackSnapshot struct {
	subscription operatorsv1alpha1.Subscription
	deployments  []string
	crds         []apiextensionsv1.CustomResourceDefinition
	groups       []operatorsv1.OperatorGroup
}

func captureRollbackSnapshot(t *testing.T, namespace, subscription, csvName string) *rollbackSnapshot {
	t.Helper()
	_, kubeClient, _ := newMigrator(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	snapshot := &rollbackSnapshot{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: subscription}, &snapshot.subscription); err != nil {
		t.Fatal(err)
	}
	var csv operatorsv1alpha1.ClusterServiceVersion
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: csvName}, &csv); err != nil {
		t.Fatal(err)
	}
	for _, deployment := range csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs {
		snapshot.deployments = append(snapshot.deployments, deployment.Name)
	}
	if len(snapshot.deployments) == 0 {
		t.Fatal("rollback scenario requires an operator Deployment")
	}
	for _, owned := range csv.Spec.CustomResourceDefinitions.Owned {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := kubeClient.Get(ctx, client.ObjectKey{Name: owned.Name}, &crd); err != nil {
			t.Fatal(err)
		}
		snapshot.crds = append(snapshot.crds, crd)
	}
	var groups operatorsv1.OperatorGroupList
	if err := kubeClient.List(ctx, &groups, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	if len(groups.Items) == 0 {
		t.Fatal("rollback scenario requires an OperatorGroup")
	}
	snapshot.groups = groups.Items
	return snapshot
}

func assertRollbackResourcesRetained(t *testing.T, snapshot *rollbackSnapshot) {
	t.Helper()
	_, kubeClient, _ := newMigrator(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for _, name := range snapshot.deployments {
		var deployment appsv1.Deployment
		if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: snapshot.subscription.Namespace, Name: name}, &deployment); err != nil {
			t.Fatalf("operator Deployment was removed: %v", err)
		}
		if deployment.DeletionTimestamp != nil {
			t.Fatalf("operator Deployment %s is being deleted", name)
		}
	}
	for i := range snapshot.crds {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&snapshot.crds[i]), &crd); err != nil {
			t.Fatal(err)
		}
		if crd.UID != snapshot.crds[i].UID || crd.DeletionTimestamp != nil {
			t.Fatalf("CRD %s was replaced or deleted", crd.Name)
		}
	}
	for i := range snapshot.groups {
		var group operatorsv1.OperatorGroup
		if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&snapshot.groups[i]), &group); err != nil {
			t.Fatal(err)
		}
		if group.UID != snapshot.groups[i].UID || group.DeletionTimestamp != nil || !reflect.DeepEqual(group.Spec, snapshot.groups[i].Spec) {
			t.Fatalf("OperatorGroup %s was replaced, deleted, or reconfigured", group.Name)
		}
	}
}

type rollbackManagement struct {
	extension ocv1.ClusterExtension
	revisions []ocv1.ClusterObjectSet
}

func captureRollbackManagement(t *testing.T, kubeClient client.Client, extension string) rollbackManagement {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var snapshot rollbackManagement
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: extension}, &snapshot.extension); err != nil {
		t.Fatal(err)
	}
	var revisions ocv1.ClusterObjectSetList
	if err := kubeClient.List(ctx, &revisions, client.MatchingLabels{migration.LabelOwnerName: extension}); err != nil {
		t.Fatal(err)
	}
	if len(revisions.Items) == 0 {
		t.Fatal("rollback scenario requires a ClusterObjectSet")
	}
	snapshot.revisions = revisions.Items
	return snapshot
}

func assertRollbackManagementRetained(t *testing.T, kubeClient client.Client, snapshot rollbackManagement) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var extension ocv1.ClusterExtension
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&snapshot.extension), &extension); err != nil {
		t.Fatal(err)
	}
	if extension.UID != snapshot.extension.UID || extension.DeletionTimestamp != nil || !reflect.DeepEqual(extension.Annotations, snapshot.extension.Annotations) {
		t.Fatal("unacknowledged rollback changed the ClusterExtension or its backups")
	}
	for i := range snapshot.revisions {
		var revision ocv1.ClusterObjectSet
		if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&snapshot.revisions[i]), &revision); err != nil {
			t.Fatal(err)
		}
		if revision.UID != snapshot.revisions[i].UID || revision.DeletionTimestamp != nil {
			t.Fatalf("unacknowledged rollback changed revision %s", revision.Name)
		}
	}
}

func assertRollbackRestored(t *testing.T, kubeClient client.Client, snapshot *rollbackSnapshot, management rollbackManagement) {
	t.Helper()
	// Subscription creation alone is not recovery. Require controller
	// reconciliation, a healthy CSV/workload, and removal of OLMv1 management.
	var pending string
	err := wait.PollUntilContextTimeout(t.Context(), 2*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		var extension ocv1.ClusterExtension
		if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&management.extension), &extension); err == nil {
			pending = fmt.Sprintf("ClusterExtension deletion (finalizers=%v)", extension.Finalizers)
			return false, nil
		} else if !apierrors.IsNotFound(err) {
			return false, err
		}
		var revisions ocv1.ClusterObjectSetList
		if err := kubeClient.List(ctx, &revisions, client.MatchingLabels{migration.LabelOwnerName: management.extension.Name}); err != nil {
			return false, err
		}
		if len(revisions.Items) != 0 {
			pending = fmt.Sprintf("ClusterObjectSet deletion (%d revisions remain)", len(revisions.Items))
			return false, nil
		}
		for i := range management.revisions {
			if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&management.revisions[i]), &ocv1.ClusterObjectSet{}); err == nil {
				pending = "ClusterObjectSet deletion (" + management.revisions[i].Name + ")"
				return false, nil
			} else if !apierrors.IsNotFound(err) {
				return false, err
			}
		}
		var sub operatorsv1alpha1.Subscription
		if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&snapshot.subscription), &sub); err != nil {
			return false, err
		}
		if !reflect.DeepEqual(sub.Spec, snapshot.subscription.Spec) {
			return false, fmt.Errorf("restored Subscription spec differs from its original backup: got %#v, want %#v", sub.Spec, snapshot.subscription.Spec)
		}
		if sub.Status.State != operatorsv1alpha1.SubscriptionStateAtLatest || sub.Status.InstalledCSV == "" {
			pending = fmt.Sprintf("OLMv0 Subscription reconciliation (state=%q, installedCSV=%q, currentCSV=%q)", sub.Status.State, sub.Status.InstalledCSV, sub.Status.CurrentCSV)
			return false, nil
		}
		var csv operatorsv1alpha1.ClusterServiceVersion
		if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: sub.Namespace, Name: sub.Status.InstalledCSV}, &csv); err != nil {
			if apierrors.IsNotFound(err) {
				pending = "restored CSV creation (" + sub.Status.InstalledCSV + ")"
			}
			return false, client.IgnoreNotFound(err)
		}
		if csv.Status.Phase != operatorsv1alpha1.CSVPhaseSucceeded {
			pending = fmt.Sprintf("restored CSV health (phase=%q, reason=%q)", csv.Status.Phase, csv.Status.Reason)
			return false, nil
		}
		for _, name := range snapshot.deployments {
			var deployment appsv1.Deployment
			if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: sub.Namespace, Name: name}, &deployment); err != nil {
				if apierrors.IsNotFound(err) {
					pending = "restored Deployment creation (" + name + ")"
				}
				return false, client.IgnoreNotFound(err)
			}
			replicas := int32(1)
			if deployment.Spec.Replicas != nil {
				replicas = *deployment.Spec.Replicas
			}
			if deployment.DeletionTimestamp != nil || deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.AvailableReplicas < replicas {
				pending = fmt.Sprintf("restored Deployment readiness (%s: available=%d, desired=%d)", name, deployment.Status.AvailableReplicas, replicas)
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("rollback did not complete (%s): %v", pending, err)
	}
	assertRollbackResourcesRetained(t, snapshot)
}
