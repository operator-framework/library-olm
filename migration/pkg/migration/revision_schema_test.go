package migration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func testRevisionSchema(deployment bool) *apiextensionsv1.CustomResourceValidation {
	var schema apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal([]byte(`{"properties":{"spec":{"properties":{"phases":{"items":{"properties":{"objects":{"items":{"properties":{"type":{"type":"string","enum":["InlineManifest","ObjectReference"]},"inlineManifest":{"type":"object"},"objectRef":{"type":"object"}}}}}}}}}}}`), &schema); err != nil {
		panic(err)
	}
	if deployment {
		schema.Properties["spec"] = apiextensionsv1.JSONSchemaProps{Properties: map[string]apiextensionsv1.JSONSchemaProps{"template": {Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": schema.Properties["spec"]}}}}
	}
	return &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &schema}
}

func TestRevisionSchemaPreflight(t *testing.T) {
	for _, name := range []string{clusterObjectSetCRDName, clusterObjectDeploymentCRDName} {
		for _, mutation := range []string{"valid", "no schema", "unserved", "old fields", "wrong union"} {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				cos, cod := establishedClusterObjectSetCRD(), establishedClusterObjectDeploymentCRD()
				crd := cos
				if name == cod.Name {
					crd = cod
				}
				switch mutation {
				case "no schema":
					crd.Spec.Versions[0].Schema = nil
				case "unserved":
					crd.Spec.Versions[0].Served = false
				case "old fields", "wrong union":
					spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
					if name == cod.Name {
						spec = spec.Properties["template"].Properties["spec"]
					}
					fields := spec.Properties["phases"].Items.Schema.Properties["objects"].Items.Schema.Properties
					if mutation == "old fields" {
						delete(fields, "inlineManifest")
					} else {
						fields["type"] = apiextensionsv1.JSONSchemaProps{Type: "string", Enum: []apiextensionsv1.JSON{{Raw: []byte(`"Old"`)}}}
					}
				}
				m := migrationTestClient(t, cos, cod)
				m.Client = interceptor.NewClient(m.Client.(client.WithWatch), interceptor.Funcs{
					Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
						t.Fatal("unexpected create during schema preflight")
						return nil
					},
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						t.Fatal("unexpected deletion during schema preflight")
						return nil
					},
				})
				_, err := m.PrepareClusterObjectSet(context.Background(), Options{ClusterExtensionName: "example", SystemNamespace: "controller"})
				if mutation == "valid" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "typed inlineManifest/objectRef") {
					t.Fatalf("expected incompatible schema, got %v", err)
				}
				if _, err := m.Gather(context.Background(), Options{ClusterExtensionName: "example"}); err == nil || !strings.Contains(err.Error(), "typed inlineManifest/objectRef") {
					t.Fatalf("Gather bypassed schema preflight: %v", err)
				}
			})
		}
	}
}
