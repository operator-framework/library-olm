package main

import (
	"testing"
)

func TestShouldUseInClusterConfig(t *testing.T) {
	testCases := []struct {
		name             string
		kubeconfig       string
		kubeconfigEnvSet bool
		want             bool
	}{
		{name: "no explicit configuration", want: true},
		{name: "flag configuration", kubeconfig: "/tmp/config", want: false},
		{name: "KUBECONFIG configuration", kubeconfigEnvSet: true, want: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := shouldUseInClusterConfig(testCase.kubeconfig, testCase.kubeconfigEnvSet); got != testCase.want {
				t.Fatalf("shouldUseInClusterConfig(%q, %t) = %t, want %t", testCase.kubeconfig, testCase.kubeconfigEnvSet, got, testCase.want)
			}
		})
	}
}

func TestFormatResultNotes(t *testing.T) {
	if got := formatResultNotes(nil); got != "" {
		t.Fatalf("formatResultNotes(nil) = %q, want empty", got)
	}
	if got, want := formatResultNotes([]string{"would delete unreferenced CatalogSource", "other note"}), "    note: would delete unreferenced CatalogSource\n    note: other note\n"; got != want {
		t.Fatalf("formatResultNotes() = %q, want %q", got, want)
	}
}
