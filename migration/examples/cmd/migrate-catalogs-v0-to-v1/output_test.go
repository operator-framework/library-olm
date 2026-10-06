package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/catalogmigration"
	"github.com/operator-framework/library-olm/migration/pkg/clioutput"
	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

func captureCatalogStdout(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = original
		_ = reader.Close()
		_ = writer.Close()
	})
	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCatalogJSONLinesProgressResultsAndError(t *testing.T) {
	oldMode := outputMode
	outputMode = clioutput.JSONL
	t.Cleanup(func() { outputMode = oldMode })

	encoded := captureCatalogStdout(t, func() {
		output := selectedCatalogOutput()
		output.start(true)
		output.progress(migration.ProgressEvent{Step: migration.ProgressStepCatalog, Status: migration.ProgressWaiting, Target: "ns/source", Message: "Waiting for catalog"})
		results := []catalogmigration.CatalogMigrationResult{
			{CatalogSourceNamespace: "ns", CatalogSourceName: "source", ClusterCatalogName: "catalog", Status: statusDryRun, Reason: "would create catalog", Notes: []string{"polling differs"}},
			{CatalogSourceNamespace: "ns", CatalogSourceName: "bad", Status: statusError, Reason: "create denied"},
		}
		err := output.results(results)
		if err == nil {
			t.Fatal("catalog result failure did not return an error")
		}
		output.fatal(err)
	})
	lines := strings.Split(strings.TrimSpace(encoded), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d JSON lines, want progress, two results, and error: %q", len(lines), encoded)
	}
	records := make([]clioutput.Record, 0, len(lines))
	for _, line := range lines {
		var record clioutput.Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if records[0].Type != "progress" || records[0].Target != "ns/source" || records[0].Step != migration.ProgressStepCatalog {
		t.Errorf("progress record = %#v", records[0])
	}
	if records[1].Type != "result" || records[1].Status != migration.ProgressCompleted || records[1].Target != "ns/source" {
		t.Errorf("dry-run result = %#v", records[1])
	}
	if data, ok := records[1].Data.(map[string]any); !ok || data["outcome"] != statusDryRun || data["cluster_catalog_name"] != "catalog" {
		t.Errorf("dry-run data = %#v", records[1].Data)
	}
	if records[2].Status != migration.ProgressFailed || records[2].Error != "create denied" || records[3].Type != "error" {
		t.Errorf("failed result and terminal error = %#v", records[2:])
	}
}

func TestCatalogTextOutputKeepsSummaryAndProgress(t *testing.T) {
	oldMode := outputMode
	outputMode = clioutput.Text
	t.Cleanup(func() { outputMode = oldMode })
	printed := captureCatalogStdout(t, func() {
		output := selectedCatalogOutput()
		output.start(true)
		output.progress(migration.ProgressEvent{Step: migration.ProgressStepScan, Status: migration.ProgressStarted, Message: "Scanning CatalogSources"})
		if err := output.results([]catalogmigration.CatalogMigrationResult{
			{CatalogSourceNamespace: "ns", CatalogSourceName: "source", ClusterCatalogName: "catalog", Status: statusDryRun, Reason: "would create"},
			{CatalogSourceNamespace: "ns", CatalogSourceName: "unsupported", Status: statusSkipped, Reason: "not migratable"},
		}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Dry run", "Scanning CatalogSources", "Would migrate", "ns/source", "Skipped", "not migratable"} {
		if !strings.Contains(printed, want) {
			t.Errorf("text output missing %q: %q", want, printed)
		}
	}
}

func TestCatalogOutputFormatValidation(t *testing.T) {
	for _, mode := range []string{clioutput.Text, clioutput.JSONL} {
		if err := outputFormats.Validate(mode); err != nil {
			t.Errorf("valid format %q rejected: %v", mode, err)
		}
	}
	if err := outputFormats.Validate("json"); err == nil || !strings.Contains(err.Error(), "json") {
		t.Fatalf("invalid format error = %v", err)
	}
	if err := clioutput.WriteError(io.Discard, io.Discard, outputFormats.Select(clioutput.Text), errors.New("failure")); err != nil {
		t.Fatal(err)
	}
}

type catalogRecordingFormatter struct{ records []clioutput.Record }

func (*catalogRecordingFormatter) Structured() bool { return true }
func (f *catalogRecordingFormatter) WriteRecord(_ io.Writer, record clioutput.Record) error {
	f.records = append(f.records, record)
	return nil
}

func TestCatalogOutputUsesRegisteredFormatter(t *testing.T) {
	formatter := &catalogRecordingFormatter{}
	oldMode := outputMode
	oldFormats := outputFormats
	outputFormats = clioutput.NewRegistry()
	outputFormats.Register("test", formatter)
	outputMode = "test"
	t.Cleanup(func() {
		outputMode = oldMode
		outputFormats = oldFormats
	})
	if err := outputFormats.Validate(outputMode); err != nil {
		t.Fatal(err)
	}
	output := selectedCatalogOutput()
	output.start(true)
	output.progress(migration.ProgressEvent{Step: migration.ProgressStepScan, Status: migration.ProgressStarted, Message: "scanning"})
	if err := output.results([]catalogmigration.CatalogMigrationResult{{CatalogSourceNamespace: "ns", CatalogSourceName: "source", Status: statusDryRun, Reason: "would create"}}); err != nil {
		t.Fatal(err)
	}
	output.fatal(errors.New("failed"))
	if len(formatter.records) != 3 || formatter.records[0].Type != "progress" || formatter.records[1].Type != "result" || formatter.records[2].Type != "error" {
		t.Fatalf("formatter records = %#v", formatter.records)
	}
}

type catalogFailingProgressFormatter struct {
	first, later error
	writes       int
}

func (*catalogFailingProgressFormatter) Structured() bool { return true }
func (f *catalogFailingProgressFormatter) WriteRecord(_ io.Writer, record clioutput.Record) error {
	if record.Type != "progress" {
		return nil
	}
	f.writes++
	if f.writes == 1 {
		return f.first
	}
	return f.later
}

func TestCatalogProgressWriteFailureWithNoResultsIsReturned(t *testing.T) {
	first := errors.New("first progress write failed")
	formatter := &catalogFailingProgressFormatter{first: first, later: errors.New("later progress write failed")}
	oldMode, oldFormats := outputMode, outputFormats
	outputFormats = clioutput.NewRegistry()
	outputFormats.Register("failing", formatter)
	outputMode = "failing"
	t.Cleanup(func() {
		outputMode, outputFormats = oldMode, oldFormats
	})

	output := selectedCatalogOutput()
	for range 2 {
		output.progress(migration.ProgressEvent{Step: migration.ProgressStepScan, Status: migration.ProgressStarted})
	}
	if err := output.results(nil); !errors.Is(err, first) {
		t.Fatalf("empty catalog results error = %v, want first progress write failure", err)
	}
	if err := output.results([]catalogmigration.CatalogMigrationResult{{Status: statusError, Reason: "catalog failed"}}); err == nil || !strings.Contains(err.Error(), "catalog source(s) failed") {
		t.Fatalf("catalog result failure was masked by progress write failure: %v", err)
	}
}
