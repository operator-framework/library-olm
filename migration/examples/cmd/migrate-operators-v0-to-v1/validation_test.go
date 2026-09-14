package main

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
)

// These validations run before constructing a Kubernetes client. Keeping them
// tested here makes invalid CLI invocations deterministic and side-effect free.
func TestCommandRejectsAmbiguousAndMissingTargets(t *testing.T) {
	t.Cleanup(func() { checkAll, convertAll, cleanupAll, rollbackAll = false, false, false, false })
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	tests := []struct {
		name string
		run  func([]string) error
	}{
		{
			name: "check",
			run: func(args []string) error {
				return runCheck(cmd, args)
			},
		},
		{
			name: "convert",
			run: func(args []string) error {
				return runConvert(cmd, args)
			},
		},
		{
			name: "cleanup",
			run: func(args []string) error {
				return runCleanup(cmd, args)
			},
		},
		{
			name: "rollback",
			run: func(args []string) error {
				return runRollback(cmd, args)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" missing target", func(t *testing.T) {
			checkAll, convertAll, cleanupAll, rollbackAll = false, false, false, false
			if err := tt.run(nil); err == nil {
				t.Fatal("missing target unexpectedly reached the Kubernetes client")
			}
		})
		t.Run(tt.name+" ambiguous target", func(t *testing.T) {
			checkAll, convertAll, cleanupAll, rollbackAll = true, true, true, true
			if err := tt.run([]string{"operator"}); err == nil {
				t.Fatal("target combined with --all unexpectedly reached the Kubernetes client")
			}
		})
	}
}
