package main

import (
	"context"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

// convertBatch shares selection and failure semantics between conversion and
// preview. Preview must never invoke the mutating migration callback.
func convertBatch(ctx context.Context, results []migration.OperatorScanResult, defaults migration.Options,
	dryRun, continueOnError bool, migrate func(context.Context, migration.Options) error, preview func(migration.Options) error,
) error {
	output := selectedCommandOutput()
	eligible := migration.EligibleFromScan(results)
	if len(eligible) == 0 {
		return output.noTargets("convert", "No eligible operators to migrate")
	}
	var firstErr error
	for _, result := range eligible {
		opts := defaults
		opts.SubscriptionName = result.SubscriptionName
		opts.SubscriptionNamespace = result.SubscriptionNamespace
		opts.ApplyDefaults()
		target := opts.SubscriptionNamespace + "/" + opts.SubscriptionName
		output.batchProcessing(target)
		var err error
		startProgress()
		if dryRun {
			err = preview(opts)
		} else {
			err = migrate(ctx, opts)
		}
		clearProgress()
		if outputErr := output.batchResult("convert", target, err); outputErr != nil {
			return outputErr
		}
		if err == nil {
			continue
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
