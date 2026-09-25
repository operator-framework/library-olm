package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sccPodSecurityLabelSync = "security.openshift.io/scc.podSecurityLabelSync"

// sourceDeploymentReplica records a live source Deployment's desired scale for
// restoration if target creation cannot complete.
type sourceDeploymentReplica struct {
	name      string
	namespace string
	replicas  int32
}

// PrepareInstallNamespace creates the requested install namespace when needed
// and copies the namespace labels that control pod security admission. It must
// run before OLMv0 management is removed so a failed preparation leaves the
// source installation untouched.
func (m *Migrator) PrepareInstallNamespace(ctx context.Context, opts Options) error {
	if opts.InstallNamespace == opts.SubscriptionNamespace {
		return nil
	}

	var source corev1.Namespace
	if err := m.Client.Get(ctx, client.ObjectKey{Name: opts.SubscriptionNamespace}, &source); err != nil {
		return fmt.Errorf("get source namespace %q: %w", opts.SubscriptionNamespace, err)
	}

	labels := securityNamespaceLabels(source.Labels)
	var target corev1.Namespace
	err := m.Client.Get(ctx, client.ObjectKey{Name: opts.InstallNamespace}, &target)
	if apierrors.IsNotFound(err) {
		target = corev1.Namespace{}
		target.Name = opts.InstallNamespace
		target.Labels = labels
		if err := m.Client.Create(ctx, &target); err != nil {
			return fmt.Errorf("create install namespace %q: %w", opts.InstallNamespace, err)
		}
		m.progress(fmt.Sprintf("Created install namespace %s with copied PSA/SCC labels", opts.InstallNamespace))
		return nil
	}
	if err != nil {
		return fmt.Errorf("get install namespace %q: %w", opts.InstallNamespace, err)
	}
	if unsafePSAEnforcement(target.Labels, labels) {
		return fmt.Errorf("source namespace PSA enforcement may weaken existing target namespace %q; choose a target with an explicit equal or weaker enforcement label", opts.InstallNamespace)
	}

	changed := false
	if target.Labels == nil && len(labels) > 0 {
		target.Labels = map[string]string{}
	}
	for key, value := range labels {
		if target.Labels[key] != value {
			target.Labels[key] = value
			changed = true
		}
	}
	if changed {
		if err := m.Client.Update(ctx, &target); err != nil {
			return fmt.Errorf("copy PSA/SCC labels to install namespace %q: %w", opts.InstallNamespace, err)
		}
		m.progress(fmt.Sprintf("Copied PSA/SCC labels to existing install namespace %s", opts.InstallNamespace))
	}
	return nil
}

// securityNamespaceLabels returns the source labels that affect workload
// admission and must accompany an operator to its new install namespace.
func securityNamespaceLabels(labels map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range labels {
		if strings.HasPrefix(key, "pod-security.kubernetes.io/") || key == sccPodSecurityLabelSync {
			result[key] = value
		}
	}
	return result
}

// unsafePSAEnforcement reports whether copying source labels would weaken an
// existing target enforce level or replace an unknown cluster default. Kubernetes
// does not expose the effective PSA default through the workload API, so an
// existing target without an enforce label must be treated conservatively.
func unsafePSAEnforcement(targetLabels, sourceLabels map[string]string) bool {
	target, targetSet := targetLabels["pod-security.kubernetes.io/enforce"]
	source, sourceSet := sourceLabels["pod-security.kubernetes.io/enforce"]
	if !sourceSet {
		return false
	}
	if !targetSet {
		return true
	}
	if target == source {
		return false
	}
	levels := map[string]int{"privileged": 0, "baseline": 1, "restricted": 2}
	return levels[source] < levels[target]
}

// RewriteInstallNamespace moves collected namespaced objects from the
// Subscription namespace to the requested install namespace. Cluster-scoped
// resources, and namespaced objects outside the source namespace, are left
// unchanged.
func RewriteInstallNamespace(objects []unstructured.Unstructured, sourceNamespace, installNamespace string) {
	if sourceNamespace == installNamespace {
		return
	}
	services, serviceAccounts := migratedNames(objects, sourceNamespace)
	for i := range objects {
		if objects[i].GetNamespace() == sourceNamespace {
			objects[i].SetNamespace(installNamespace)
			clearServiceAllocations(&objects[i])
		}
		rewriteServiceReferences(&objects[i], sourceNamespace, installNamespace, services, serviceAccounts)
	}
}

// migratedNames indexes source Services and ServiceAccounts whose references
// may need retargeting after their namespace changes.
func migratedNames(objects []unstructured.Unstructured, sourceNamespace string) (map[string]struct{}, map[string]struct{}) {
	services := map[string]struct{}{}
	serviceAccounts := map[string]struct{}{}
	for i := range objects {
		if objects[i].GetNamespace() != sourceNamespace {
			continue
		}
		switch objects[i].GetKind() {
		case "Service":
			services[objects[i].GetName()] = struct{}{}
		case "ServiceAccount":
			serviceAccounts[objects[i].GetName()] = struct{}{}
		}
	}
	return services, serviceAccounts
}

// rewriteServiceReferences updates cluster-scoped references to Services that
// move with the operator. These references remain valid through cutover after
// the source Service is deleted.
func rewriteServiceReferences(obj *unstructured.Unstructured, sourceNamespace, installNamespace string, services, serviceAccounts map[string]struct{}) {
	if obj.GetKind() == "RoleBinding" || obj.GetKind() == "ClusterRoleBinding" {
		rewriteServiceAccountSubjects(obj, sourceNamespace, installNamespace, serviceAccounts)
	}
	switch obj.GetKind() {
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		webhooks, found, err := unstructured.NestedSlice(obj.Object, "webhooks")
		if err != nil || !found {
			return
		}
		for i := range webhooks {
			webhook, ok := webhooks[i].(map[string]interface{})
			if !ok {
				continue
			}
			rewriteNestedServiceNamespace(webhook, sourceNamespace, installNamespace, services, "clientConfig", "service")
		}
		_ = unstructured.SetNestedSlice(obj.Object, webhooks, "webhooks")
	case "CustomResourceDefinition":
		rewriteNestedServiceNamespace(obj.Object, sourceNamespace, installNamespace, services, "spec", "conversion", "webhook", "clientConfig", "service")
	}
}

// rewriteServiceAccountSubjects updates explicit ServiceAccount subjects that
// refer to a collected account moved into the target namespace.
func rewriteServiceAccountSubjects(obj *unstructured.Unstructured, sourceNamespace, installNamespace string, serviceAccounts map[string]struct{}) {
	subjects, found, err := unstructured.NestedSlice(obj.Object, "subjects")
	if err != nil || !found {
		return
	}
	for i := range subjects {
		subject, ok := subjects[i].(map[string]interface{})
		if !ok || subject["kind"] != "ServiceAccount" || subject["namespace"] != sourceNamespace {
			continue
		}
		name, _ := subject["name"].(string)
		if _, found := serviceAccounts[name]; !found {
			continue
		}
		subject["namespace"] = installNamespace
	}
	_ = unstructured.SetNestedSlice(obj.Object, subjects, "subjects")
}

// rewriteNestedServiceNamespace retargets a collected Service reference when
// the reference's namespace and name both identify a moved Service.
func rewriteNestedServiceNamespace(object map[string]interface{}, sourceNamespace, installNamespace string, services map[string]struct{}, serviceFields ...string) {
	if len(serviceFields) == 0 {
		return
	}
	nameFields := append(append([]string{}, serviceFields...), "name")
	namespaceFields := append(append([]string{}, serviceFields...), "namespace")
	name, nameFound, nameErr := unstructured.NestedString(object, nameFields...)
	namespace, namespaceFound, namespaceErr := unstructured.NestedString(object, namespaceFields...)
	if nameErr == nil && namespaceErr == nil && nameFound && namespaceFound && namespace == sourceNamespace {
		if _, found := services[name]; found {
			_ = unstructured.SetNestedField(object, installNamespace, namespaceFields...)
		}
	}
}

// clearServiceAllocations removes values Kubernetes allocated to a Service in
// its source namespace. clusterIP and nodePort are cluster-wide allocations;
// retaining either when moving a Service to another namespace makes the target
// Service conflict with the still-running source Service before cutover.
func clearServiceAllocations(obj *unstructured.Unstructured) {
	if obj.GetAPIVersion() != "v1" || obj.GetKind() != "Service" {
		return
	}

	clusterIP, _, _ := unstructured.NestedString(obj.Object, "spec", "clusterIP")
	if clusterIP != "None" {
		unstructured.RemoveNestedField(obj.Object, "spec", "clusterIP")
		unstructured.RemoveNestedField(obj.Object, "spec", "clusterIPs")
	}
	// A health-check NodePort is allocated by Kubernetes just like each Service
	// port's nodePort. Keep the desired Service shape but allocate fresh ports.
	unstructured.RemoveNestedField(obj.Object, "spec", "healthCheckNodePort")
	ports, found, err := unstructured.NestedSlice(obj.Object, "spec", "ports")
	if err != nil || !found {
		return
	}
	for i := range ports {
		port, ok := ports[i].(map[string]interface{})
		if !ok {
			continue
		}
		delete(port, "nodePort")
	}
	_ = unstructured.SetNestedSlice(obj.Object, ports, "spec", "ports")
}

// DeleteSourceNamespaceResources removes the old copies of objects that were
// applied in a different install namespace. It runs only after the target CE
// has reached Installed=True, so a failed handoff retains the OLMv0 workload.
// It intentionally does not delete the Namespace itself; that requires the
// separate AcknowledgeNamespaceDelete opt-in.
func (m *Migrator) DeleteSourceNamespaceResources(ctx context.Context, objects []unstructured.Unstructured, opts Options) error {
	if opts.InstallNamespace == opts.SubscriptionNamespace {
		return nil
	}
	deleted := 0
	for i := range objects {
		obj := objects[i].DeepCopy()
		if obj.GetNamespace() != opts.SubscriptionNamespace {
			continue
		}
		uid := obj.GetUID()
		if uid == "" {
			return fmt.Errorf("refuse to delete source resource %s %s/%s without collected UID", obj.GetKind(), obj.GetNamespace(), obj.GetName())
		}
		if err := m.Client.Delete(ctx, obj, client.Preconditions(metav1.Preconditions{UID: &uid})); err != nil && client.IgnoreNotFound(err) != nil {
			if apierrors.IsConflict(err) {
				return fmt.Errorf("source resource %s %s/%s changed since collection: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
			}
			return fmt.Errorf("delete source resource %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
		deleted++
	}
	if deleted > 0 {
		m.progress(fmt.Sprintf("Deleted %d migrated resource(s) from source namespace %s", deleted, opts.SubscriptionNamespace))
	}
	return nil
}

// ScaleSourceDeployments stops collected source Deployments before the target
// namespace starts them. It returns a restore function for use when target
// creation fails, so a failed cutover does not leave the source operator down.
func (m *Migrator) ScaleSourceDeployments(ctx context.Context, objects []unstructured.Unstructured, opts Options) (func(context.Context) error, error) {
	noRestore := func(context.Context) error { return nil }
	if opts.InstallNamespace == opts.SubscriptionNamespace {
		return noRestore, nil
	}

	var originals []sourceDeploymentReplica
	for i := range objects {
		obj := objects[i]
		if obj.GetAPIVersion() != "apps/v1" || obj.GetKind() != "Deployment" || obj.GetNamespace() != opts.SubscriptionNamespace {
			continue
		}
		var original *sourceDeploymentReplica
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			var deployment appsv1.Deployment
			if err := m.Client.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: obj.GetName()}, &deployment); err != nil {
				return err
			}
			replicas := int32(1)
			if deployment.Spec.Replicas != nil {
				replicas = *deployment.Spec.Replicas
			}
			if replicas == 0 {
				return nil
			}
			zero := int32(0)
			deployment.Spec.Replicas = &zero
			if err := m.Client.Update(ctx, &deployment); err != nil {
				return err
			}
			original = &sourceDeploymentReplica{name: deployment.Name, namespace: deployment.Namespace, replicas: replicas}
			return nil
		}); err != nil {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			restoreErr := restoreSourceDeploymentReplicas(restoreCtx, m, originals)
			cancel()
			if restoreErr != nil {
				return noRestore, fmt.Errorf("scale source Deployment %s/%s: %w; restore scaled Deployments: %v", obj.GetNamespace(), obj.GetName(), err, restoreErr)
			}
			return noRestore, fmt.Errorf("scale source Deployment %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}
		if original != nil {
			originals = append(originals, *original)
		}
	}

	return func(restoreCtx context.Context) error {
		return restoreSourceDeploymentReplicas(restoreCtx, m, originals)
	}, nil
}

// restoreSourceDeploymentReplicas returns scaled source Deployments to their
// pre-cutover replica counts.
func restoreSourceDeploymentReplicas(ctx context.Context, m *Migrator, originals []sourceDeploymentReplica) error {
	var errs []error
	for _, original := range originals {
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			var deployment appsv1.Deployment
			if err := m.Client.Get(ctx, client.ObjectKey{Namespace: original.namespace, Name: original.name}, &deployment); err != nil {
				return err
			}
			replicas := original.replicas
			deployment.Spec.Replicas = &replicas
			return m.Client.Update(ctx, &deployment)
		}); err != nil {
			errs = append(errs, fmt.Errorf("restore source Deployment %s/%s: %w", original.namespace, original.name, err))
		}
	}
	return errors.Join(errs...)
}
