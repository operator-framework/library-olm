package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

func TestCOSWaitsRejectLegacyConditions(t *testing.T) {
	cos := &ocv1.ClusterObjectSet{
		ObjectMeta: metav1.ObjectMeta{Name: "example-1"},
		Status: ocv1.ClusterObjectSetStatus{Conditions: []metav1.Condition{
			{Type: "Succeeded", Status: metav1.ConditionTrue},
			{Type: "Available", Status: metav1.ConditionTrue},
		}},
	}
	m := migrationTestClient(t, cos)
	for name, wait := range map[string]func(context.Context, string) error{
		"completion": m.WaitForCOSSucceeded,
		"readiness":  m.WaitForClusterObjectSetAvailable,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := wait(ctx, cos.Name); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("legacy conditions must not satisfy wait: %v", err)
			}
		})
	}
}

func TestCOSCompletionInvalidSpecification(t *testing.T) {
	for _, generation := range []int64{1, 2} {
		cos := &ocv1.ClusterObjectSet{
			ObjectMeta: metav1.ObjectMeta{Name: "example-1", Generation: 2},
			Status: ocv1.ClusterObjectSetStatus{Conditions: []metav1.Condition{{
				Type: ocv1.ClusterObjectSetTypeReady, Status: metav1.ConditionFalse,
				Reason:  ocv1.ClusterObjectSetReasonInvalidSpecification,
				Message: "invalid reference", ObservedGeneration: generation,
			}}},
		}
		m := migrationTestClient(t, cos)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := m.WaitForCOSSucceeded(ctx, cos.Name)
		cancel()
		if generation == cos.Generation {
			if err == nil || !strings.Contains(err.Error(), "invalid reference") {
				t.Fatalf("expected current failure diagnostic, got %v", err)
			}
		} else if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stale condition must not terminate wait: %v", err)
		}
	}
}
