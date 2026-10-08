package main

import (
	"context"
	"strings"
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

func TestConvertAllRejectsSingleOperatorFlags(t *testing.T) {
	oldAll, oldNamespace, oldCEName := convertAll, convertNamespace, convertCEName
	t.Cleanup(func() {
		convertAll, convertNamespace, convertCEName = oldAll, oldNamespace, oldCEName
	})
	cmd := &cobra.Command{}
	for _, tt := range []struct {
		name, namespace, ceName, want string
	}{
		{name: "namespace", namespace: "operators", want: "--all scans every namespace"},
		{name: "CE name", ceName: "custom", want: "--ce-name cannot be combined with --all"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			convertAll = true
			convertNamespace, convertCEName = tt.namespace, tt.ceName
			if err := runConvert(cmd, nil); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runConvert(--all) error = %v, want %q before client creation", err, tt.want)
			}
		})
	}
	for _, flag := range []string{"namespace", "ce-name"} {
		t.Run("explicit empty "+flag, func(t *testing.T) {
			convertAll = true
			convertNamespace, convertCEName = "", ""
			cmd := &cobra.Command{}
			cmd.Flags().String(flag, "", "")
			if err := cmd.Flags().Set(flag, ""); err != nil {
				t.Fatal(err)
			}
			if err := runConvert(cmd, nil); err == nil || !strings.Contains(err.Error(), "cannot be combined with --all") {
				t.Fatalf("runConvert(--all --%s '') error = %v, want flag rejection", flag, err)
			}
		})
	}
}

func TestOperatorNameMatchingCommandIsPositionalArgument(t *testing.T) {
	for _, tt := range []struct {
		command, operator string
	}{
		{command: "convert", operator: "check"},
		{command: "check", operator: "convert"},
	} {
		t.Run(tt.command+" "+tt.operator, func(t *testing.T) {
			cmd, args, err := rootCmd.Find([]string{tt.command, tt.operator, "-n", "operators"})
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Name() != tt.command {
				t.Fatalf("selected command = %q, want %q", cmd.Name(), tt.command)
			}
			namespaceFlag := cmd.Flags().Lookup("namespace")
			if namespaceFlag == nil {
				t.Fatal("selected command has no namespace flag")
			}
			// Parse the arguments on a disposable command so the global CLI
			// command's flag values, changed bits, and parsed args stay untouched.
			parser := &cobra.Command{}
			parser.Flags().StringP(namespaceFlag.Name, namespaceFlag.Shorthand, "", "")
			if err := parser.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			positionals := parser.Flags().Args()
			if err := cmd.ValidateArgs(positionals); err != nil {
				t.Fatal(err)
			}
			if len(positionals) != 1 || positionals[0] != tt.operator {
				t.Fatalf("positional arguments = %q, want operator %q", positionals, tt.operator)
			}
		})
	}
}
