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
