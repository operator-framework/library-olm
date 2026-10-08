package migration

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	ocv1ac "github.com/operator-framework/operator-controller/applyconfigurations/api/v1"
)

func largeConfigMap(name, payload string) *ocv1ac.ClusterObjectSetObjectApplyConfiguration {
	object := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": name},
		"data":     map[string]interface{}{"payload": payload},
	}}
	return ocv1ac.ClusterObjectSetObject().WithObject(object)
}

func TestSecretPackerSplitsLargeBundleAcrossSecrets(t *testing.T) {
	payload := strings.Repeat("x", 350*1024)
	phase := ocv1ac.ClusterObjectSetPhase().WithObjects(
		largeConfigMap("first", payload),
		largeConfigMap("second", payload),
		largeConfigMap("third", payload),
	)
	result, err := (&secretPacker{RevisionName: "revision", OwnerName: "extension", SystemNamespace: "operator-system"}).pack([]*ocv1ac.ClusterObjectSetPhaseApplyConfiguration{phase})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Secrets) != 2 || len(result.Refs) != 3 {
		t.Fatalf("packed %d Secrets and %d refs, want 2 and 3", len(result.Secrets), len(result.Refs))
	}
	if result.Refs[[2]int{0, 0}].Name != result.Refs[[2]int{0, 1}].Name || result.Refs[[2]int{0, 1}].Name == result.Refs[[2]int{0, 2}].Name {
		t.Fatalf("large bundle was not split at the size limit: %#v", result.Refs)
	}
	for _, secret := range result.Secrets {
		if secret.Namespace != "operator-system" || secret.Labels[LabelRevisionName] != "revision" || secret.Labels[LabelOwnerName] != "extension" || secret.Immutable == nil || !*secret.Immutable {
			t.Errorf("incorrect packed Secret metadata: %#v", secret.ObjectMeta)
		}
		size := 0
		for _, data := range secret.Data {
			size += len(data)
		}
		if size > maxSecretDataSize {
			t.Errorf("Secret %s has %d data bytes, over %d-byte limit", secret.Name, size, maxSecretDataSize)
		}
	}
}

func TestSecretPackerCompressesOversizedObject(t *testing.T) {
	payload := strings.Repeat("x", gzipThreshold+1)
	phase := ocv1ac.ClusterObjectSetPhase().WithObjects(largeConfigMap("large", payload))
	result, err := (&secretPacker{RevisionName: "revision", OwnerName: "extension", SystemNamespace: "operator-system"}).pack([]*ocv1ac.ClusterObjectSetPhaseApplyConfiguration{phase})
	if err != nil || len(result.Secrets) != 1 {
		t.Fatalf("packing compressible object: %#v, %v", result, err)
	}
	ref := result.Refs[[2]int{0, 0}]
	packed := result.Secrets[0].Data[ref.Key]
	if !bytes.HasPrefix(packed, []byte{0x1f, 0x8b}) {
		t.Fatalf("large object was not gzip-compressed: %d bytes", len(packed))
	}
	reader, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var restored unstructured.Unstructured
	if err := json.Unmarshal(decoded, &restored); err != nil {
		t.Fatal(err)
	}
	data, found, err := unstructured.NestedString(restored.Object, "data", "payload")
	if err != nil || !found || data != payload {
		t.Fatalf("restored payload length = %d, found = %t, error = %v", len(data), found, err)
	}
}
