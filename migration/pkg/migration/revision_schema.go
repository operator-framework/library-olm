package migration

import (
	"fmt"
	"slices"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// validateRevisionSchema rejects incompatible experimental APIs before migration
// deletes OLMv0 resources. It validates capabilities, not controller versions.
func validateRevisionSchema(crd *apiextensionsv1.CustomResourceDefinition, deployment bool) error {
	for _, version := range crd.Spec.Versions {
		if version.Name != ocv1.GroupVersion.Version || !version.Served || version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
			continue
		}
		spec := version.Schema.OpenAPIV3Schema.Properties["spec"]
		if deployment {
			spec = spec.Properties["template"].Properties["spec"]
		}
		phases := spec.Properties["phases"]
		if phases.Items == nil || phases.Items.Schema == nil {
			break
		}
		objects := phases.Items.Schema.Properties["objects"]
		if objects.Items == nil || objects.Items.Schema == nil {
			break
		}
		fields := objects.Items.Schema.Properties
		typeField := fields["type"]
		accepts := func(value string) bool {
			return typeField.Type == "string" && (len(typeField.Enum) == 0 || slices.ContainsFunc(typeField.Enum, func(e apiextensionsv1.JSON) bool { return string(e.Raw) == fmt.Sprintf("%q", value) }))
		}
		if fields["inlineManifest"].Type == "object" && fields["objectRef"].Type == "object" &&
			accepts(string(ocv1.ObjectTypeInlineManifest)) && accepts(string(ocv1.ObjectTypeObjectReference)) {
			return nil
		}
		break
	}
	return fmt.Errorf("CRD %s must serve %s with typed inlineManifest/objectRef revision entries; install matching experimental operator-controller definitions", crd.Name, ocv1.GroupVersion.Version)
}
