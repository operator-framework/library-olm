package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogCLIUsageOutput(t *testing.T) {
	missingKubeconfig := filepath.Join(t.TempDir(), "missing-kubeconfig")
	for _, tt := range []struct {
		name      string
		args      []string
		wantUsage bool
		wantError string
	}{
		{name: "unexpected argument", args: []string{"source"}, wantUsage: true, wantError: "unknown command"},
		{name: "invalid flag", args: []string{"--unknown"}, wantUsage: true, wantError: "unknown flag"},
		{name: "invalid output format", args: []string{"--output=invalid"}, wantUsage: true, wantError: "invalid --output"},
		{name: "runtime failure", args: []string{"--kubeconfig", missingKubeconfig}, wantError: "failed to get REST config"},
		{name: "structured runtime failure", args: []string{"--kubeconfig", missingKubeconfig, "--output=jsonl"}, wantError: `"type":"error"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestCatalogCLIUsageHelperProcess$", "--"}, tt.args...)
			cmd := exec.CommandContext(t.Context(), os.Args[0], args...) //nolint:gosec // runs this test binary with fixed CLI test arguments
			cmd.Env = append(os.Environ(), "LIBRARY_OLM_CATALOG_USAGE_TEST_HELPER=1")
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("command unexpectedly succeeded: %s", output)
			}
			if got := strings.Contains(string(output), "Usage:"); got != tt.wantUsage {
				t.Fatalf("usage present = %t, want %t; output:\n%s", got, tt.wantUsage, output)
			}
			if !strings.Contains(string(output), tt.wantError) {
				t.Fatalf("output missing %q:\n%s", tt.wantError, output)
			}
		})
	}
}

func TestCatalogCLIUsageHelperProcess(t *testing.T) {
	t.Helper()
	if os.Getenv("LIBRARY_OLM_CATALOG_USAGE_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			rootCmd.SetArgs(os.Args[i+1:])
			break
		}
	}
	if err := rootCmd.Execute(); err != nil {
		selectedCatalogOutput().fatal(err)
		os.Exit(1)
	}
}
