package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/operator-framework/library-olm/migration/pkg/clioutput"
	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

func TestRecoveryCleanupStructuredOutput(t *testing.T) {
	formatter := &recordingFormatter{}
	oldMode, oldFormats := outputMode, outputFormats
	outputFormats = clioutput.NewRegistry()
	outputFormats.Register("recording", formatter)
	outputMode = "recording"
	t.Cleanup(func() { outputMode, outputFormats = oldMode, oldFormats })

	result := &migration.RecoveryCleanupResult{
		ClusterExtensionName: "widgets",
		Annotations:          []string{migration.MigrationSubscriptionBackupAnnotation},
		RollbackLoss:         true,
		DryRun:               true,
	}
	output := selectedCommandOutput()
	if err := output.recoveryResult(result); err != nil {
		t.Fatal(err)
	}
	completed := *result
	completed.DryRun = false
	if err := output.recoveryResult(&completed); err != nil {
		t.Fatal(err)
	}
	if len(formatter.records) != 2 || formatter.records[0].Type != "preview" || formatter.records[1].Type != "result" {
		t.Fatalf("records = %#v", formatter.records)
	}
	for _, record := range formatter.records {
		if record.Command != "cleanup-recovery" || record.Target != "widgets" || !reflect.DeepEqual(record.Data, result) && !reflect.DeepEqual(record.Data, &completed) {
			t.Fatalf("record = %#v", record)
		}
	}
}

func TestRecoveryCleanupCommandValidation(t *testing.T) {
	if cleanupRecoveryCmd.Name() != "cleanup-recovery" {
		t.Fatal("recovery cleanup command is missing")
	}
	if err := cleanupRecoveryCmd.Args(cleanupRecoveryCmd, nil); err == nil {
		t.Fatal("missing ClusterExtension name accepted")
	}
	if err := cleanupRecoveryCmd.Args(cleanupRecoveryCmd, []string{"one", "two"}); err == nil {
		t.Fatal("multiple ClusterExtension names accepted")
	}
	if err := validateRecoveryCleanupArguments(cleanupRecoveryCmd, []string{""}); err == nil || !strings.Contains(err.Error(), "ClusterExtension name") {
		t.Fatalf("empty ClusterExtension name error = %v", err)
	}
}
