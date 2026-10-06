package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/operator-framework/library-olm/migration/pkg/catalogmigration"
	"github.com/operator-framework/library-olm/migration/pkg/clioutput"
	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

type catalogOutput struct {
	start    func(dryRun bool)
	progress migration.ProgressFunc
	results  func([]catalogmigration.CatalogMigrationResult) error
	fatal    func(error)
}

type catalogResultRecord struct {
	CatalogSourceNamespace string   `json:"catalog_source_namespace"`
	CatalogSourceName      string   `json:"catalog_source_name"`
	ClusterCatalogName     string   `json:"cluster_catalog_name,omitempty"`
	Outcome                string   `json:"outcome"`
	Reason                 string   `json:"reason,omitempty"`
	Notes                  []string `json:"notes,omitempty"`
}

var outputFormats = clioutput.NewRegistry()

func selectedCatalogOutput() catalogOutput {
	formatter := outputFormats.Select(outputMode)
	pending := ""
	var progressWriteErr error
	output := catalogOutput{
		start: func(dryRun bool) {
			if dryRun {
				fmt.Printf("\n🔍 Dry run — no cluster changes will be made.\n\n")
			} else {
				fmt.Printf("\n🔄 Migrating CatalogSources to ClusterCatalogs...\n\n")
			}
		},
		progress: func(event migration.ProgressEvent) {
			clioutput.WriteTextProgress(os.Stdout, event, true, &pending)
		},
		results: reportCatalogText,
		fatal: func(err error) {
			_ = clioutput.WriteError(os.Stdout, os.Stderr, formatter, err)
		},
	}
	if formatter.Structured() {
		output.start = func(bool) {}
		output.progress = func(event migration.ProgressEvent) {
			if err := formatter.WriteRecord(os.Stdout, clioutput.ProgressRecord("migrate-catalogs", "", event)); err != nil && progressWriteErr == nil {
				progressWriteErr = fmt.Errorf("write catalog progress: %w", err)
			}
		}
		output.results = func(results []catalogmigration.CatalogMigrationResult) error {
			if err := reportCatalogStructured(formatter, results); err != nil {
				return err
			}
			return progressWriteErr
		}
		output.fatal = func(err error) {
			_ = clioutput.WriteError(os.Stdout, os.Stderr, formatter, err)
		}
	}
	return output
}

func reportCatalogStructured(formatter clioutput.Formatter, results []catalogmigration.CatalogMigrationResult) error {
	failures := 0
	for _, result := range results {
		var resultErr error
		if result.Status == statusError {
			resultErr = errors.New(result.Reason)
			failures++
		}
		record := clioutput.ResultRecord("migrate-catalogs", result.CatalogSourceNamespace+"/"+result.CatalogSourceName, resultErr)
		if result.Status == statusSkipped {
			record.Status = migration.ProgressWarning
		}
		record.Message = result.Reason
		record.Data = catalogResultRecord{
			CatalogSourceNamespace: result.CatalogSourceNamespace,
			CatalogSourceName:      result.CatalogSourceName,
			ClusterCatalogName:     result.ClusterCatalogName,
			Outcome:                result.Status, Reason: result.Reason, Notes: result.Notes,
		}
		if err := formatter.WriteRecord(os.Stdout, record); err != nil {
			return err
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d catalog source(s) failed to migrate", failures)
	}
	return nil
}
