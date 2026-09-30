package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ocv1 "github.com/operator-framework/operator-controller/api/v1"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

func TestDryRunRejectsIncompatibleInputs(t *testing.T) {
	for _, name := range []string{"apiservice", "dependency", "generated-subscription", "wrong-acknowledgment"} {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, register := range []func(*runtime.Scheme) error{corev1.AddToScheme, operatorsv1.AddToScheme, operatorsv1alpha1.AddToScheme, ocv1.AddToScheme, apiextensionsv1.AddToScheme} {
				if err := register(scheme); err != nil {
					t.Fatal(err)
				}
			}
			sub := &operatorsv1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: "widgets", Namespace: "operators"},
				Spec:   &operatorsv1alpha1.SubscriptionSpec{Package: "widgets"},
				Status: operatorsv1alpha1.SubscriptionStatus{InstalledCSV: "widgets.v1", State: operatorsv1alpha1.SubscriptionStateAtLatest}}
			csv := &operatorsv1alpha1.ClusterServiceVersion{ObjectMeta: metav1.ObjectMeta{Name: "widgets.v1", Namespace: "operators"},
				Status: operatorsv1alpha1.ClusterServiceVersionStatus{Phase: operatorsv1alpha1.CSVPhaseSucceeded, Reason: operatorsv1alpha1.CSVReasonInstallSuccessful}}
			og := &operatorsv1.OperatorGroup{ObjectMeta: metav1.ObjectMeta{Name: "operators", Namespace: "operators"}}
			opts := migration.Options{SubscriptionName: sub.Name, SubscriptionNamespace: sub.Namespace, SystemNamespace: "controller", AcknowledgeWatchScopeChange: true, AcknowledgeOperatorCondition: true, AcknowledgeOLMv0APIAccess: true, AcknowledgeScopedServiceAccount: true, AcknowledgeNotSteadyState: true}
			switch name {
			case "apiservice":
				csv.Spec.APIServiceDefinitions.Owned = []operatorsv1alpha1.APIServiceDescription{{Name: "v1.widgets.example.com"}}
			case "dependency":
				csv.Annotations = map[string]string{"operatorframework.io/properties": `[{"type":"olm.package.required","value":{"packageName":"other"}}]`}
			case "generated-subscription":
				sub.Annotations = map[string]string{"olm.generated-by": "parent"}
			case "wrong-acknowledgment":
				og.Spec.ServiceAccountName = "scoped"
				opts.AcknowledgeScopedServiceAccount = false
			}
			crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "clusterobjectsets.olm.operatorframework.io"}, Status: apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sub, csv, og, crd).Build()
			m := migration.NewMigrator(c, nil)
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			err := runConvertDryRun(cmd, m, opts)
			if err == nil || !strings.Contains(err.Error(), "not eligible") {
				t.Fatalf("dry-run error = %v, want eligibility refusal", err)
			}
			for _, obj := range []client.Object{sub, csv, og} {
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
					t.Fatal(err)
				}
			}
			var extensions ocv1.ClusterExtensionList
			if err := c.List(t.Context(), &extensions); err != nil {
				t.Fatal(err)
			}
			if len(extensions.Items) != 0 {
				t.Fatal("rejected preview created a ClusterExtension")
			}
		})
	}
}
