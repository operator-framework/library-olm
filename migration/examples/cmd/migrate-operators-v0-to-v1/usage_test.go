package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIUsageOutput(t *testing.T) {
	missingKubeconfig := filepath.Join(t.TempDir(), "missing-kubeconfig")
	for _, tt := range []struct {
		name      string
		args      []string
		wantUsage bool
		wantError string
	}{
		{name: "missing target", args: []string{"convert"}, wantUsage: true, wantError: "specify an operator name or --all"},
		{name: "invalid flag", args: []string{"convert", "--unknown"}, wantUsage: true, wantError: "unknown flag"},
		{name: "too many targets", args: []string{"convert", "one", "two", "-n", "operators"}, wantUsage: true, wantError: "accepts at most 1 arg"},
		{name: "invalid output", args: []string{"convert", "one", "-n", "operators", "--output=invalid"}, wantUsage: true, wantError: "invalid --output"},
		{name: "invalid namespace flags", args: []string{"convert", "one", "-n", "operators", "--install-namespace", "other", "--system-managed-install-namespace"}, wantUsage: true, wantError: "cannot be combined"},
		{name: "convert runtime failure", args: []string{"convert", "one", "-n", "operators", "--kubeconfig", missingKubeconfig}, wantError: "failed to get REST config"},
		{name: "check runtime failure", args: []string{"check", "one", "-n", "operators", "--kubeconfig", missingKubeconfig}, wantError: "failed to get REST config"},
		{name: "rollback runtime failure", args: []string{"rollback", "one", "--kubeconfig", missingKubeconfig}, wantError: "failed to get REST config"},
		{name: "cleanup runtime failure", args: []string{"cleanup", "one", "--kubeconfig", missingKubeconfig}, wantError: "failed to get REST config"},
		{name: "structured runtime failure", args: []string{"convert", "one", "-n", "operators", "--kubeconfig", missingKubeconfig, "--output=jsonl"}, wantError: `"type":"error"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestCLIUsageHelperProcess$", "--"}, tt.args...)
			cmd := exec.CommandContext(t.Context(), os.Args[0], args...) //nolint:gosec // executes this test binary with fixed CLI test arguments
			cmd.Env = append(os.Environ(), "LIBRARY_OLM_USAGE_TEST_HELPER=1")
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

func TestCLIUsageHelperProcess(t *testing.T) {
	t.Helper()
	if os.Getenv("LIBRARY_OLM_USAGE_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			rootCmd.SetArgs(os.Args[i+1:])
			break
		}
	}
	if err := rootCmd.Execute(); err != nil {
		selectedCommandOutput().fatalError(err)
		os.Exit(1)
	}
}
