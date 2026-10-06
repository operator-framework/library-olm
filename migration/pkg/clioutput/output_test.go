package clioutput

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if err := WriteError(&out, &errOut, Text, errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "Error: failed") {
		t.Fatalf("text error = %q", errOut.String())
	}
}
