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
		info("No eligible operators to migrate.")
		return nil
	}
	var firstErr error
	for _, result := range eligible {
		opts := defaults
		opts.SubscriptionName = result.SubscriptionName
		opts.SubscriptionNamespace = result.SubscriptionNamespace
		opts.ApplyDefaults()
		info(fmt.Sprintf("Processing %s/%s...", opts.SubscriptionNamespace, opts.SubscriptionName))
		var err error
		if dryRun {
			err = preview(opts)
		} else {
			err = migrate(ctx, opts)
		}
		if err == nil {
			continue
		}
		fail(fmt.Sprintf("%s/%s: %v", opts.SubscriptionNamespace, opts.SubscriptionName, err))
		if !continueOnError {
			return err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
