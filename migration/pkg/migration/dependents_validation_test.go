package migration

import (
	"context"
	"reflect"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
)

func TestFindDependentsOnlyReportsInstalledOperatorsRequiringPackage(t *testing.T) {
	objects := []struct {
		name, namespace, packageName, installedCSV, properties string
	}{
		{name: "consumer", namespace: "first", packageName: "consumer", installedCSV: "consumer.v1", properties: `[{"type":"olm.package.required","value":{"packageName":"widgets"}}]`},
		{name: "wrapped", namespace: "second", packageName: "another", installedCSV: "another.v1", properties: `{"properties":[{"type":"olm.package.required","value":{"packageName":"widgets"}}]}`},
		{name: "uninstalled", namespace: "third", packageName: "pending", properties: `[{"type":"olm.package.required","value":{"packageName":"widgets"}}]`},
		{name: "unrelated", namespace: "fourth", packageName: "other", installedCSV: "other.v1", properties: `[{"type":"olm.package.required","value":{"packageName":"different"}}]`},
		{name: "malformed", namespace: "fifth", packageName: "broken", installedCSV: "broken.v1", properties: "not JSON"},
		{name: "widgets-op", namespace: "zero", packageName: "widgets", installedCSV: "widgets.v1", properties: `[{"type":"olm.package.required","value":{"packageName":"widgets"}}]`},
	}
	var resources []runtime.Object
	for _, item := range objects {
		resources = append(resources, &operatorsv1alpha1.Subscription{
			ObjectMeta: metav1.ObjectMeta{Name: item.name, Namespace: item.namespace},
			Spec:       &operatorsv1alpha1.SubscriptionSpec{Package: item.packageName},
			Status:     operatorsv1alpha1.SubscriptionStatus{InstalledCSV: item.installedCSV},
		})
		if item.installedCSV != "" {
			resources = append(resources, &operatorsv1alpha1.ClusterServiceVersion{
				ObjectMeta: metav1.ObjectMeta{Name: item.installedCSV, Namespace: item.namespace, Annotations: map[string]string{"operatorframework.io/properties": item.properties}},
			})
		}
	}
	m := migrationTestClient(t, resources...)
	got := m.findDependents(context.Background(), "widgets")
	sort.Strings(got)
	if want := []string{"first/consumer", "second/wrapped"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("findDependents() = %q, want %q", got, want)
	}
}
