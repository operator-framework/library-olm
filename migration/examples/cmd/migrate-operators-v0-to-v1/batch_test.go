package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/clioutput"
	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

func TestConvertBatchFailsWhenProgressCannotBeWritten(t *testing.T) {
	cause := errors.New("progress output failed")
	formatter := &failingProgressFormatter{first: cause, later: cause}
	oldMode, oldFormats := outputMode, outputFormats
	outputFormats = clioutput.NewRegistry()
	outputFormats.Register("failing", formatter)
	outputMode = "failing"
	t.Cleanup(func() {
		outputMode, outputFormats = oldMode, oldFormats
	})

	results := []migration.OperatorScanResult{{SubscriptionName: "widget", SubscriptionNamespace: "operators", Status: migration.OperatorStatusEligible}}
	err := convertBatch(t.Context(), results, migration.Options{}, false, false,
		func(context.Context, migration.Options) error {
			progressFuncFor("convert", "operators/widget")(migration.ProgressEvent{Step: migration.ProgressStepProfile, Status: migration.ProgressStarted})
			return nil
		},
		func(migration.Options) error { t.Fatal("unexpected preview"); return nil })
	if !errors.Is(err, cause) {
		t.Fatalf("batch error = %v, want progress write failure", err)
	}
	if len(formatter.records) != 1 || formatter.records[0].Status != migration.ProgressFailed {
		t.Fatalf("batch reported success despite progress failure: %#v", formatter.records)
	}
}

func TestConvertBatchJSONLines(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout, originalMode := os.Stdout, outputMode
	os.Stdout, outputMode = writer, "jsonl"
	t.Cleanup(func() {
		os.Stdout, outputMode = originalStdout, originalMode
		_ = reader.Close()
		_ = writer.Close()
	})

	results := []migration.OperatorScanResult{{SubscriptionName: "widget", SubscriptionNamespace: "operators", Status: migration.OperatorStatusEligible}}
	if err := convertBatch(t.Context(), results, migration.Options{}, false, false,
		func(context.Context, migration.Options) error { return nil },
		func(migration.Options) error { t.Fatal("unexpected preview"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var record outputRecord
	if err := json.Unmarshal(output, &record); err != nil {
		t.Fatalf("batch output is not one JSON record: %q: %v", output, err)
	}
	if record.Type != "result" || record.Command != "convert" || record.Target != "operators/widget" || record.Status != migration.ProgressCompleted {
		t.Fatalf("unexpected batch result: %#v", record)
	}
}

func TestConvertBatchDryRunJSONLinesIncludesResult(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout, originalMode := os.Stdout, outputMode
	os.Stdout, outputMode = writer, "jsonl"
	t.Cleanup(func() {
		os.Stdout, outputMode = originalStdout, originalMode
		_ = reader.Close()
		_ = writer.Close()
	})

	results := []migration.OperatorScanResult{{SubscriptionName: "widget", SubscriptionNamespace: "operators", Status: migration.OperatorStatusEligible}}
	if err := convertBatch(t.Context(), results, migration.Options{}, true, false,
		func(context.Context, migration.Options) error { t.Fatal("dry-run invoked migration"); return nil },
		func(migration.Options) error {
			return writeOutputRecord(outputRecord{Type: "dry-run", Command: "convert", Target: "operators/widget"})
		}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 {
		t.Fatalf("batch dry-run output has %d records, want preview and result: %q", len(lines), output)
	}
	var preview, result outputRecord
	if err := json.Unmarshal([]byte(lines[0]), &preview); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &result); err != nil {
		t.Fatal(err)
	}
	if preview.Type != "dry-run" || result.Type != "result" || result.Command != "convert" || result.Target != "operators/widget" || result.Status != migration.ProgressCompleted {
		t.Fatalf("unexpected batch dry-run records: preview=%#v result=%#v", preview, result)
	}
}

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
