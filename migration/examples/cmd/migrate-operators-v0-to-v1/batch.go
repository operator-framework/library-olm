package main

import (
	"context"
	"fmt"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

// convertBatch shares selection and failure semantics between conversion and
// preview. Preview must never invoke the mutating migration callback.
func convertBatch(ctx context.Context, results []migration.OperatorScanResult, defaults migration.Options,
	dryRun, continueOnError bool, migrate func(context.Context, migration.Options) error, preview func(migration.Options) error,
) error {
	eligible := migration.EligibleFromScan(results)
	if len(eligible) == 0 {
		if jsonOutput() {
			return writeOutputRecord(outputRecord{Type: "result", Command: "convert", Status: migration.ProgressCompleted, Message: "No eligible operators to migrate"})
		}
		info("No eligible operators to migrate.")
		return nil
	}
	var firstErr error
	for _, result := range eligible {
		opts := defaults
		opts.SubscriptionName = result.SubscriptionName
		opts.SubscriptionNamespace = result.SubscriptionNamespace
		opts.ApplyDefaults()
		target := opts.SubscriptionNamespace + "/" + opts.SubscriptionName
		if !jsonOutput() {
			info(fmt.Sprintf("Processing %s...", target))
		}
		var err error
		startProgress()
		if dryRun {
			err = preview(opts)
		} else {
			err = migrate(ctx, opts)
		}
		clearProgress()
		if jsonOutput() && (!dryRun || err != nil) {
			record := outputRecord{Type: "result", Command: "convert", Target: target, Status: migration.ProgressCompleted}
			if err != nil {
				record.Status = migration.ProgressFailed
				record.Error = err.Error()
			}
			if writeErr := writeOutputRecord(record); writeErr != nil {
				return writeErr
			}
		}
		if err == nil {
			continue
		}
		if !jsonOutput() {
			fail(fmt.Sprintf("%s: %v", target, err))
		}
		if !continueOnError {
			return err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
