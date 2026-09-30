package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

func TestConvertBatchSafety(t *testing.T) {
	firstFailure := errors.New("first migration failed")
	results := []migration.OperatorScanResult{
		{SubscriptionName: "already", Status: migration.OperatorStatusAlreadyMigrated},
		{SubscriptionName: "first", SubscriptionNamespace: "one", Status: migration.OperatorStatusEligible},
		{SubscriptionName: "conflict", Status: migration.OperatorStatusConflict, Eligible: true},
		{SubscriptionName: "ineligible", Status: migration.OperatorStatusIneligible, Eligible: true},
		{SubscriptionName: "second", SubscriptionNamespace: "two", Status: migration.OperatorStatusEligible},
		{SubscriptionName: "third", SubscriptionNamespace: "three", Status: migration.OperatorStatusEligible},
	}
	for _, tc := range []struct {
		name                          string
		dryRun, continueOnError, fail bool
		want                          []string
	}{
		{name: "eligible only", want: []string{"first", "second", "third"}},
		{name: "stop at first failure", fail: true, want: []string{"first"}},
		{name: "continue and retain first failure", continueOnError: true, fail: true, want: []string{"first", "second", "third"}},
		{name: "preview never migrates", dryRun: true, want: []string{"first", "second", "third"}},
		{name: "preview stops on failure", dryRun: true, fail: true, want: []string{"first"}},
		{name: "preview continues on failure", dryRun: true, continueOnError: true, fail: true, want: []string{"first", "second", "third"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			defaults := migration.Options{BackupDirectory: "backups", DeleteOperatorGroup: true, AcknowledgeNotSteadyState: true}
			action := func(opts migration.Options) error {
				calls = append(calls, opts.SubscriptionName)
				if opts.BackupDirectory != defaults.BackupDirectory || !opts.DeleteOperatorGroup || !opts.AcknowledgeNotSteadyState || opts.InstallNamespace != opts.SubscriptionNamespace || opts.ClusterExtensionName != opts.SubscriptionName {
					t.Fatalf("batch lost options/defaults: %#v", opts)
				}
				if tc.fail && opts.SubscriptionName == "first" {
					return firstFailure
				}
				if tc.fail && opts.SubscriptionName == "second" {
					return errors.New("second migration failed")
				}
				return nil
			}
			err := convertBatch(t.Context(), results, defaults, tc.dryRun, tc.continueOnError,
				func(ctx context.Context, opts migration.Options) error {
					if tc.dryRun {
						t.Fatal("dry-run invoked migration")
					}
					if ctx != t.Context() {
						t.Fatal("lost command context")
					}
					return action(opts)
				}, func(opts migration.Options) error {
					if !tc.dryRun {
						t.Fatal("conversion invoked preview")
					}
					return action(opts)
				})
			if tc.fail && !errors.Is(err, firstFailure) || !tc.fail && err != nil {
				t.Fatalf("batch error: %v", err)
			}
			if !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("calls = %v, want %v", calls, tc.want)
			}
		})
	}
}

func TestBatchWithNoEligibleOperators(t *testing.T) {
	for _, results := range [][]migration.OperatorScanResult{nil, {{Status: migration.OperatorStatusConflict}}} {
		err := convertBatch(t.Context(), results, migration.Options{}, false, false,
			func(context.Context, migration.Options) error { t.Fatal("unexpected migration"); return nil },
			func(migration.Options) error { t.Fatal("unexpected preview"); return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBatchSummaryOrdering(t *testing.T) {
	var report strings.Builder
	results := []migration.OperatorScanResult{
		{Status: migration.OperatorStatusEligible}, {Status: migration.OperatorStatusAlreadyMigrated},
		{Status: migration.OperatorStatusIneligible}, {Status: migration.OperatorStatusConflict},
	}
	migration.PrintScanSummary(results, func(format string, args ...interface{}) { fmt.Fprintf(&report, format, args...) })
	previous := -1
	for _, state := range []string{"Conflict", "Ineligible", "AlreadyMigrated", "Eligible"} {
		index := strings.Index(report.String(), "=== "+state+" (1) ===")
		if index <= previous {
			t.Fatalf("incorrect section ordering: %s", report.String())
		}
		previous = index
	}
}
