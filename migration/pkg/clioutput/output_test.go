package clioutput

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

func TestProgressRecordUsesEventTargetAndError(t *testing.T) {
	event := migration.ProgressEvent{
		Step: migration.ProgressStepCatalog, Status: migration.ProgressFailed,
		Target: "catalogs/source", Message: "catalog failed", Err: errors.New("denied"),
	}
	record := ProgressRecord("migrate-catalogs", "", event)
	if record.Type != "progress" || record.Target != event.Target || record.Error != "denied" {
		t.Fatalf("progress record = %#v", record)
	}
	var out bytes.Buffer
	if err := WriteJSONLine(&out, record); err != nil {
		t.Fatal(err)
	}
	var decoded Record
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Target != event.Target || decoded.Error != "denied" {
		t.Fatalf("decoded record = %#v", decoded)
	}
}

func TestTextProgressAndError(t *testing.T) {
	var out, errOut bytes.Buffer
	pending := ""
	WriteTextProgress(&out, migration.ProgressEvent{Step: migration.ProgressStepCatalog, Status: migration.ProgressWaiting, Message: "waiting"}, true, &pending)
	WriteTextProgress(&out, migration.ProgressEvent{Step: migration.ProgressStepCatalog, Status: migration.ProgressWarning, Message: "backup warning", Err: errors.New("denied")}, true, &pending)
	ClearTextProgress(&out, &pending)
	if pending != "" || !strings.Contains(out.String(), "backup warning: denied") {
		t.Fatalf("text progress = %q, pending = %q", out.String(), pending)
	}
	if err := WriteError(&out, &errOut, NewRegistry().Select(Text), errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "Error: failed") {
		t.Fatalf("text error = %q", errOut.String())
	}
}

type testFormatter struct{ records []Record }

func (*testFormatter) Structured() bool { return true }
func (f *testFormatter) WriteRecord(_ io.Writer, record Record) error {
	f.records = append(f.records, record)
	return nil
}

func TestRegistrySupportsAdditionalStructuredFormats(t *testing.T) {
	registry := NewRegistry()
	formatter := &testFormatter{}
	registry.Register("yaml", formatter)
	if names := strings.Join(registry.Names(), ","); names != "jsonl,text,yaml" {
		t.Fatalf("format names = %q", names)
	}
	if err := registry.Validate("yaml"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Select("yaml").WriteRecord(io.Discard, Record{Type: "result"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteError(io.Discard, io.Discard, registry.Select("yaml"), errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if len(formatter.records) != 2 || formatter.records[0].Type != "result" || formatter.records[1].Type != "error" {
		t.Fatalf("formatter records = %#v", formatter.records)
	}
	if err := registry.Validate("unknown"); err == nil || !strings.Contains(err.Error(), "yaml") {
		t.Fatalf("invalid format error = %v", err)
	}
	if registry.Select("unknown").Structured() {
		t.Fatal("invalid format should fall back to text")
	}
}
