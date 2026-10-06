package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

type recordingFormatter struct {
	records []outputRecord
}

func (*recordingFormatter) structured() bool { return true }

func (f *recordingFormatter) writeRecord(record outputRecord) error {
	f.records = append(f.records, record)
	return nil
}

func TestOutputFormatterRegistration(t *testing.T) {
	formatter := &recordingFormatter{}
	oldMode := outputMode
	outputFormatters["test"] = formatter
	outputMode = "test"
	t.Cleanup(func() {
		outputMode = oldMode
		delete(outputFormatters, "test")
	})

	if err := validateOutputFormat(); err != nil || !structuredOutput() {
		t.Fatalf("registered format was not selected: %v", err)
	}
	progressFuncFor("convert", "operators/widget")(migration.ProgressEvent{
		Step: migration.ProgressStepProfile, Status: migration.ProgressStarted, Message: "Profiling operator",
	})
	if err := writeOutputRecord(outputRecord{Type: "result", Command: "convert", Target: "operators/widget"}); err != nil {
		t.Fatal(err)
	}
	if len(formatter.records) != 2 || formatter.records[0].Type != "progress" || formatter.records[1].Type != "result" {
		t.Fatalf("registered formatter received %#v", formatter.records)
	}

	outputMode = "unsupported"
	if err := validateOutputFormat(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("invalid format validation = %v", err)
	}
	if structuredOutput() {
		t.Fatal("invalid output mode should use plain-text error reporting")
	}
}

func TestCommandOutputUsesSelectedFormatter(t *testing.T) {
	formatter := &recordingFormatter{}
	oldMode := outputMode
	outputFormatters["test"] = formatter
	outputMode = "test"
	t.Cleanup(func() {
		outputMode = oldMode
		delete(outputFormatters, "test")
	})

	output := selectedCommandOutput()
	result := migration.OperatorScanResult{SubscriptionNamespace: "operators", SubscriptionName: "widget", Status: migration.OperatorStatusEligible}
	if err := output.scanResults("check", []migration.OperatorScanResult{result}); err != nil {
		t.Fatal(err)
	}
	if err := output.checkResult("check", "operators/widget", result); err != nil {
		t.Fatal(err)
	}
	if err := output.noTargets("convert", "No eligible operators to migrate"); err != nil {
		t.Fatal(err)
	}
	if err := output.batchResult("convert", "operators/widget", errors.New("migration failed")); err != nil {
		t.Fatal(err)
	}
	if err := output.singleResult("rollback", "widget", nil); err != nil {
		t.Fatal(err)
	}
	if err := output.dryRunDone("operators/widget"); err != nil {
		t.Fatal(err)
	}

	records := formatter.records
	if len(records) != 6 {
		t.Fatalf("got %d records, want 6: %#v", len(records), records)
	}
	if records[0].Type != "scan" || records[0].Command != "check" || records[1].Type != "check" || records[1].Target != "operators/widget" {
		t.Errorf("scan/check records = %#v", records[:2])
	}
	if records[2].Type != "result" || records[2].Message != "No eligible operators to migrate" {
		t.Errorf("no-targets record = %#v", records[2])
	}
	if records[3].Status != migration.ProgressFailed || records[3].Error != "migration failed" {
		t.Errorf("failed batch record = %#v", records[3])
	}
	if records[4].Command != "rollback" || records[4].Status != migration.ProgressCompleted || records[5].Command != "convert" || records[5].Status != migration.ProgressCompleted {
		t.Errorf("completion records = %#v", records[4:])
	}
}

func TestProgressFuncRendersStructuredEvents(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = reader.Close()
		_ = writer.Close()
		progressRunning = false
		progressMsg = ""
	})

	startProgress()
	progressFunc(migration.ProgressEvent{Step: migration.ProgressStepProfile, Status: migration.ProgressStarted, Message: "Profiling operator"})
	progressFunc(migration.ProgressEvent{Step: migration.ProgressStepCatalog, Status: migration.ProgressWaiting, Message: "Querying catalog"})
	progressFunc(migration.ProgressEvent{Step: migration.ProgressStepBackup, Status: migration.ProgressWarning, Message: "Backup failed", Err: errors.New("disk full")})
	progressFunc(migration.ProgressEvent{Step: migration.ProgressStepProfile, Status: migration.ProgressCompleted})
	clearProgress()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Profiling operator", "Querying catalog", "Backup failed: disk full", "profile complete"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("progress output missing %q: %q", want, output)
		}
	}
}

func TestProgressFuncJSONLines(t *testing.T) {
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
		progressRunning = false
		progressMsg = ""
	})

	startProgress()
	progressFuncFor("convert", "operators/widget")(migration.ProgressEvent{Step: migration.ProgressStepProfile, Status: migration.ProgressStarted, Message: "Profiling operator"})
	progressFunc(migration.ProgressEvent{Step: migration.ProgressStepBackup, Status: migration.ProgressWarning, Message: "Backup failed", Err: errors.New("disk full")})
	clearProgress()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSON lines, want 2: %q", len(lines), output)
	}
	var started, warning outputRecord
	if err := json.Unmarshal([]byte(lines[0]), &started); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &warning); err != nil {
		t.Fatal(err)
	}
	if started.Type != "progress" || started.Command != "convert" || started.Target != "operators/widget" || started.Step != migration.ProgressStepProfile || started.Status != migration.ProgressStarted {
		t.Errorf("unexpected start record: %#v", started)
	}
	if warning.Status != migration.ProgressWarning || warning.Error != "disk full" || warning.Message != "Backup failed" {
		t.Errorf("unexpected warning record: %#v", warning)
	}
}

func TestScanResultDataKeepsStructuredDiagnostics(t *testing.T) {
	data := scanResultData(migration.OperatorScanResult{
		SubscriptionNamespace: "operators", SubscriptionName: "widget", Status: migration.OperatorStatusIneligible,
		Checks: []migration.CheckResult{{Name: "api", Passed: false, Message: "missing"}}, Error: errors.New("lookup failed"),
	})
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"namespace":"operators"`, `"checks":[{"name":"api","passed":false,"message":"missing"}]`, `"error":"lookup failed"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("scan JSON missing %s: %s", want, encoded)
		}
	}
}
