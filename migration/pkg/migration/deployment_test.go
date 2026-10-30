package migration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

func readyDeployment() *ocv1.ClusterObjectDeployment {
	return &ocv1.ClusterObjectDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "deployment", Generation: 2},
		Status: ocv1.ClusterObjectDeploymentStatus{Conditions: []metav1.Condition{
			{Type: ocv1.ClusterObjectDeploymentTypeProgressing, Status: metav1.ConditionFalse, Reason: ocv1.ClusterObjectDeploymentReasonNewClusterObjectSetProgressed, ObservedGeneration: 2},
			{Type: ocv1.ClusterObjectDeploymentTypeAvailable, Status: metav1.ConditionTrue, ObservedGeneration: 2},
		}},
	}
}

func TestDeploymentReadiness(t *testing.T) {
	for _, tc := range []struct {
		name        string
		change      func(*ocv1.ClusterObjectDeployment)
		wantError   bool
		wantMessage string
	}{
		{name: "completed and available", change: func(*ocv1.ClusterObjectDeployment) {}},
		{name: "stale completion", change: func(c *ocv1.ClusterObjectDeployment) { c.Status.Conditions[0].ObservedGeneration = 1 }, wantError: true},
		{name: "stale availability", change: func(c *ocv1.ClusterObjectDeployment) { c.Status.Conditions[1].ObservedGeneration = 1 }, wantError: true},
		{name: "still progressing", change: func(c *ocv1.ClusterObjectDeployment) { c.Status.Conditions[0].Status = metav1.ConditionTrue }, wantError: true},
		{name: "unavailable", change: func(c *ocv1.ClusterObjectDeployment) { c.Status.Conditions[1].Status = metav1.ConditionFalse }, wantError: true},
		{name: "deadline exceeded", change: func(c *ocv1.ClusterObjectDeployment) {
			c.Status.Conditions[0].Reason = ocv1.ClusterObjectDeploymentReasonProgressDeadlineExceeded
			c.Status.Conditions[0].Message = "rollout exceeded its progress deadline"
		}, wantError: true, wantMessage: "rollout exceeded its progress deadline"},
		{name: "replaced", change: func(c *ocv1.ClusterObjectDeployment) { c.UID = "replacement" }, wantError: true},
		{name: "template changed", change: func(c *ocv1.ClusterObjectDeployment) { c.Generation++ }, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := readyDeployment()
			current := created.DeepCopy()
			tc.change(current)
			m := migrationTestClient(t, current)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			err := m.waitForDeployment(ctx, created)
			if (err != nil) != tc.wantError {
				t.Fatalf("wait error = %v, want error %v", err, tc.wantError)
			}
			if tc.wantMessage != "" {
				if !strings.Contains(err.Error(), tc.wantMessage) {
					t.Fatalf("wait error = %v, want diagnostic %q", err, tc.wantMessage)
				}
				if ctx.Err() != nil {
					t.Fatalf("wait exhausted context instead of failing promptly: %v", ctx.Err())
				}
			}
		})
	}
}

func TestPrepareDeployment(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: clusterObjectDeploymentCRDName}, Status: apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}}}
	ctx := context.Background()
	if err := migrationTestClient(t).prepareClusterObjectDeployment(ctx, "example"); err == nil {
		t.Fatal("accepted missing CRD")
	}
	if err := migrationTestClient(t, crd).prepareClusterObjectDeployment(ctx, "example"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "Upper", "1example", "trailing-", strings.Repeat("a", 53)} {
		if err := migrationTestClient(t, crd).prepareClusterObjectDeployment(ctx, name); err == nil {
			t.Fatalf("accepted invalid name %q", name)
		}
	}
	if err := migrationTestClient(t, crd, readyDeployment()).prepareClusterObjectDeployment(ctx, "example"); err == nil {
		t.Fatal("accepted existing deployment")
	}
	for _, revision := range []*ocv1.ClusterObjectSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "other"}, Spec: ocv1.ClusterObjectSetSpec{Group: "example"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Labels: map[string]string{LabelOwnerName: "example"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "example-1"}},
	} {
		if err := migrationTestClient(t, crd, revision).prepareClusterObjectDeployment(ctx, "example"); err == nil {
			t.Fatal("accepted occupied revision group")
		}
	}
	crd.Status.Conditions = nil
	if err := migrationTestClient(t, crd).prepareClusterObjectDeployment(ctx, "example"); err == nil {
		t.Fatal("accepted unestablished CRD")
	}
}

func TestDeploymentInvalidSpecification(t *testing.T) {
	cod := readyDeployment()
	cod.Status.Conditions[0].Reason = ocv1.ClusterObjectDeploymentReasonInvalidSpecification
	cod.Status.Conditions[0].Message = "invalid template annotation"
	m := migrationTestClient(t, cod)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := m.waitForDeployment(ctx, cod)
	if err == nil || !strings.Contains(err.Error(), "invalid template annotation") || ctx.Err() != nil {
		t.Fatalf("expected immediate diagnostic, got %v (context: %v)", err, ctx.Err())
	}
	cod.Status.Conditions[0].ObservedGeneration--
	m = migrationTestClient(t, cod)
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.waitForDeployment(ctx, cod); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale invalid specification must not fail current rollout: %v", err)
	}
}

func TestAutomaticDeploymentSelection(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		present, established, failDiscovery bool
	}{
		{name: "absent uses COS"},
		{name: "present uses COD", present: true, established: true},
		{name: "unready does not fall back", present: true},
		{name: "discovery failure does not fall back", failDiscovery: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cosCRD := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: clusterObjectSetCRDName}, Status: apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}}}
			m := migrationTestClient(t, cosCRD)
			if tc.present {
				codCRD := cosCRD.DeepCopy()
				codCRD.Name = clusterObjectDeploymentCRDName
				codCRD.ResourceVersion = ""
				if !tc.established {
					codCRD.Status.Conditions = nil
				}
				if err := m.Client.Create(context.Background(), codCRD); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failDiscovery {
				m.Client = interceptor.NewClient(m.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if key.Name == clusterObjectDeploymentCRDName {
						return errors.New("discovery denied")
					}
					return c.Get(ctx, key, obj, opts...)
				}})
			}
			// An input value must not override automatic selection.
			opts, err := m.PrepareClusterObjectSet(context.Background(), Options{SubscriptionName: "example", SystemNamespace: "controller", UseClusterObjectDeployment: !tc.present})
			wantError := tc.failDiscovery || tc.present && !tc.established
			if (err != nil) != wantError {
				t.Fatalf("error = %v, want error %v", err, wantError)
			}
			if err == nil && opts.UseClusterObjectDeployment != tc.present {
				t.Fatalf("COD selected = %v, want %v", opts.UseClusterObjectDeployment, tc.present)
			}
		})
	}
}

func TestRollbackDeploymentOwnership(t *testing.T) {
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "extension", Annotations: map[string]string{MigratedFromSubscriptionAnnotation: "ns/sub"}}}
	for _, tc := range []struct {
		name        string
		owner       *metav1.OwnerReference
		annotations map[string]string
		wantError   bool
	}{
		{name: "controlled", owner: metav1.NewControllerRef(ce, ocv1.GroupVersion.WithKind(ocv1.ClusterExtensionKind))},
		{name: "foreign controller", owner: &metav1.OwnerReference{APIVersion: ocv1.GroupVersion.String(), Kind: ocv1.ClusterExtensionKind, Name: ce.Name, UID: "foreign", Controller: func() *bool { b := true; return &b }()}, wantError: true},
		{name: "migration staging", annotations: map[string]string{migrationInvocationAnnotation: "invocation", MigratedFromSubscriptionAnnotation: "ns/sub"}},
		{name: "unowned", wantError: true},
		{name: "different subscription", annotations: map[string]string{migrationInvocationAnnotation: "invocation", MigratedFromSubscriptionAnnotation: "other/sub"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cod := readyDeployment()
			cod.Annotations = tc.annotations
			if tc.owner != nil {
				cod.OwnerReferences = []metav1.OwnerReference{*tc.owner}
			}
			m := migrationTestClient(t, cod)
			_, err := m.rollbackDeployment(context.Background(), ce)
			if (err != nil) != tc.wantError {
				t.Fatalf("ownership error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}

type completingDeploymentClient struct{ client.Client }

func (c completingDeploymentClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if cod, ok := obj.(*ocv1.ClusterObjectDeployment); ok {
		ready := readyDeployment()
		cod.UID, cod.Generation, cod.Status = ready.UID, ready.Generation, ready.Status
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestCreateDeploymentTemplate(t *testing.T) {
	m := migrationTestClient(t)
	m.Client = completingDeploymentClient{m.Client}
	resources := &createdMigrationResources{}
	err := m.createClusterObjectDeployment(context.Background(), Options{ClusterExtensionName: "example", SubscriptionNamespace: "ns", SubscriptionName: "sub"}, &MigrationInfo{PackageName: "package", BundleName: "bundle", Version: "1.0"}, nil, "invocation", resources)
	if err != nil {
		t.Fatal(err)
	}
	if resources.cod == nil || resources.cod.Spec.Template.Spec.CollisionProtection != ocv1.CollisionProtectionNone {
		t.Fatalf("unexpected deployment: %+v", resources.cod)
	}
	if resources.cod.Annotations[migrationInvocationAnnotation] != "invocation" {
		t.Fatal("missing recovery ownership marker")
	}
	if !reflect.DeepEqual(resources.cod.Spec.Template.Metadata.Annotations, resources.cod.Annotations) {
		t.Fatal("template must propagate bundle and migration annotations to revisions")
	}
	if !reflect.DeepEqual(resources.cod.Spec.Template.Metadata.Labels, resources.cod.Labels) {
		t.Fatal("template must propagate extension identity labels to revisions")
	}
	var revisions ocv1.ClusterObjectSetList
	if err := m.Client.List(context.Background(), &revisions); err != nil {
		t.Fatal(err)
	}
	if len(revisions.Items) != 0 {
		t.Fatal("migration created revisions instead of delegating to COD")
	}
	if err := m.createClusterObjectDeployment(context.Background(), Options{ClusterExtensionName: "example"}, &MigrationInfo{}, nil, "other", &createdMigrationResources{}); err == nil {
		t.Fatal("overwrote existing deployment")
	}
}

func TestRollbackRejectsForeignRevisionsBeforeStoppingDeployment(t *testing.T) {
	for _, tc := range []struct {
		name, revisionName, group, owner string
		controller                       *metav1.OwnerReference
	}{
		{name: "foreign initial group", revisionName: "example-1", group: "foreign"},
		{name: "foreign initial owner", revisionName: "example-1", owner: "foreign"},
		{name: "conflicting group member", revisionName: "example-2", group: "example", owner: "foreign"},
		{name: "foreign COD controller", revisionName: "example-2", group: "example", controller: metav1.NewControllerRef(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "foreign"}}, ocv1.GroupVersion.WithKind(ocv1.ClusterObjectDeploymentKind))},
		{name: "foreign CE controller", revisionName: "example-1", group: "example", controller: metav1.NewControllerRef(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "foreign"}}, ocv1.GroupVersion.WithKind(ocv1.ClusterExtensionKind))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "extension", Annotations: map[string]string{
				MigratedFromSubscriptionAnnotation:    "source/sub",
				MigrationSubscriptionBackupAnnotation: `{"name":"example","source":"catalog","sourceNamespace":"olm"}`,
			}}}
			cod := readyDeployment()
			cod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(ce, ocv1.GroupVersion.WithKind(ocv1.ClusterExtensionKind))}
			revision := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: tc.revisionName, Labels: map[string]string{LabelOwnerName: tc.owner}}, Spec: ocv1.ClusterObjectSetSpec{Group: tc.group}}
			if tc.controller != nil {
				revision.OwnerReferences = []metav1.OwnerReference{*tc.controller}
			}
			m := migrationTestClient(t, ce, cod, revision, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "source"}})
			// Reject every mutation, including the finalizer update that precedes deletion.
			m.Client = interceptor.NewClient(m.Client.(client.WithWatch), interceptor.Funcs{
				Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
					t.Fatal("rollback updated resources before validating revision ownership")
					return nil
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					t.Fatal("rollback deleted resources before validating revision ownership")
					return nil
				},
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					t.Fatal("rollback restored OLMv0 despite a foreign revision")
					return nil
				},
			})
			if err := m.Rollback(ctx, Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true}); err == nil || (!strings.Contains(err.Error(), "belongs to another") && !strings.Contains(err.Error(), "controlled by another")) {
				t.Fatalf("rollback error = %v, want foreign revision rejection", err)
			}
		})
	}
}

func TestRollbackRevisionControllerOwnership(t *testing.T) {
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "extension"}}
	cod := readyDeployment()
	cod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(ce, ocv1.GroupVersion.WithKind(ocv1.ClusterExtensionKind))}
	for _, tc := range []struct {
		name       string
		owner      *metav1.OwnerReference
		missingCOD bool
		wantError  bool
	}{
		{name: "orphaned or legacy revision"},
		{name: "CE controller", owner: metav1.NewControllerRef(ce, ocv1.GroupVersion.WithKind(ocv1.ClusterExtensionKind))},
		{name: "COD controller", owner: metav1.NewControllerRef(cod, ocv1.GroupVersion.WithKind(ocv1.ClusterObjectDeploymentKind))},
		{name: "missing COD cannot verify UID", owner: metav1.NewControllerRef(cod, ocv1.GroupVersion.WithKind(ocv1.ClusterObjectDeploymentKind)), missingCOD: true, wantError: true},
		{name: "wrong API group", owner: metav1.NewControllerRef(cod, corev1.SchemeGroupVersion.WithKind(ocv1.ClusterObjectDeploymentKind)), wantError: true},
		{name: "wrong kind", owner: metav1.NewControllerRef(cod, ocv1.GroupVersion.WithKind("Other")), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := migrationTestClient(t, ce)
			if !tc.missingCOD {
				if err := m.Client.Create(context.Background(), cod.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			revision := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "example-2"}, Spec: ocv1.ClusterObjectSetSpec{Group: ce.Name}}
			if tc.owner != nil {
				revision.OwnerReferences = []metav1.OwnerReference{*tc.owner}
			}
			if err := m.validateRollbackRevision(context.Background(), revision, ce); (err != nil) != tc.wantError {
				t.Fatalf("validation error = %v, want error %v", err, tc.wantError)
			}
		})
	}
	// The old initial revision may lack both group and label; other unowned
	// objects without any migration identity must not be admitted by name alone.
	m := migrationTestClient(t, ce)
	for _, name := range []string{"example-1", "unrelated"} {
		revision := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := m.validateRollbackRevision(context.Background(), revision, ce); (err != nil) != (name == "unrelated") {
			t.Fatalf("validation of %s = %v", name, err)
		}
	}
}

func TestRollbackDeploymentAndUnlabelledRevisions(t *testing.T) {
	ctx := context.Background()
	ce := &ocv1.ClusterExtension{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "extension", Annotations: map[string]string{
		MigratedFromSubscriptionAnnotation:    "source/sub",
		MigrationSubscriptionBackupAnnotation: `{"name":"example","source":"catalog","sourceNamespace":"olm"}`,
	}}}
	cod := readyDeployment()
	cod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(ce, ocv1.GroupVersion.WithKind(ocv1.ClusterExtensionKind))}
	first := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "example-1", UID: "first"}, Spec: ocv1.ClusterObjectSetSpec{Group: ce.Name}}
	second := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "example-2", UID: "second"}, Spec: ocv1.ClusterObjectSetSpec{Group: ce.Name}}
	unrelated := &ocv1.ClusterObjectSet{ObjectMeta: metav1.ObjectMeta{Name: "other-1"}, Spec: ocv1.ClusterObjectSetSpec{Group: "other"}}
	m := migrationTestClient(t, ce, cod, first, second, unrelated, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "source"}})
	recorder := &deploymentDeletionClient{Client: m.Client, t: t}
	m.Client = recorder
	if err := m.Rollback(ctx, Options{ClusterExtensionName: ce.Name, AcknowledgeInstalled: true}); err != nil {
		t.Fatal(err)
	}
	if !recorder.stopped {
		t.Fatal("deployment was not stopped")
	}
	for _, obj := range []client.Object{ce, cod, first, second} {
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); !apierrors.IsNotFound(err) {
			t.Fatalf("retained %T: %v", obj, err)
		}
	}
	if err := m.Client.Get(ctx, client.ObjectKeyFromObject(unrelated), unrelated); err != nil {
		t.Fatal(err)
	}
	if err := m.Client.Get(ctx, client.ObjectKey{Namespace: "source", Name: "sub"}, &operatorsv1alpha1.Subscription{}); err != nil {
		t.Fatal(err)
	}
}

type deploymentDeletionClient struct {
	client.Client
	t       *testing.T
	stopped bool
}

func (c *deploymentDeletionClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	options := (&client.DeleteOptions{}).ApplyOptions(opts)
	switch obj.(type) {
	case *ocv1.ClusterObjectDeployment:
		if options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationOrphan {
			c.t.Fatal("deployment deletion must orphan revisions")
		}
		c.stopped = true
	case *ocv1.ClusterObjectSet:
		if !c.stopped {
			c.t.Fatal("deleted revision while deployment was still active")
		}
	}
	return c.Client.Delete(ctx, obj, opts...)
}
