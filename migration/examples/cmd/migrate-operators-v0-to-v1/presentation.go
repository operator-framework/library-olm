package main

import (
	"fmt"
	"os"

	"github.com/operator-framework/library-olm/migration/pkg/clioutput"
	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

// commandOutput is selected once per command. Its callbacks keep output-format
// choices out of the migration, preview, and error-handling paths.
type commandOutput struct {
	scanStart       func(command string)
	scanResults     func(command string, results []migration.OperatorScanResult) error
	checkStart      func(target string)
	checkResult     func(command, target string, result migration.OperatorScanResult) error
	dryRunStart     func(target string)
	dryRunChecks    func(target string, result migration.OperatorScanResult) error
	dryRunPreview   func(opts, resourceOpts migration.Options, info *migration.MigrationInfo) error
	dryRunDone      func(target string) error
	convertStart    func(target string)
	convertDone     func(target, name string) error
	noTargets       func(command, message string) error
	batchStart      func(command string, count int)
	batchProcessing func(target string)
	batchResult     func(command, target string, resultErr error) error
	singleStart     func(command, target string)
	singleResult    func(command, target string, resultErr error) error
	fatalError      func(error)
}

func selectedCommandOutput() commandOutput {
	formatter := selectedFormatter()
	output := commandOutput{
		scanStart: func(command string) {
			message := "Scanning all Subscriptions..."
			if command == "convert" {
				message = "Scanning all Subscriptions for migration..."
			}
			fmt.Printf("\n%s%s🔎 %s%s\n", colorBold, colorCyan, message, colorReset)
		},
		scanResults: func(_ string, results []migration.OperatorScanResult) error {
			migration.PrintScanSummary(results, func(format string, a ...interface{}) { fmt.Printf(format, a...) })
			return nil
		},
		checkStart: func(target string) {
			fmt.Printf("\n%s%s🔍 Pre-migration checks for %s%s\n", colorBold, colorCyan, target, colorReset)
		},
		checkResult: func(_, _ string, result migration.OperatorScanResult) error {
			sectionHeader("Readiness, Compatibility and Catalog Checks")
			printCheckResults(result.Checks)
			if result.Status == migration.OperatorStatusEligible {
				success(result.Reason)
			} else {
				fail(fmt.Sprintf("%s: %s", result.Status, result.Reason))
			}
			for _, warning := range result.Warnings {
				warn(warning)
			}
			fmt.Println()
			return nil
		},
		dryRunStart: func(target string) {
			fmt.Printf("\n%s%s🔍 Dry run: %s%s\n", colorBold, colorCyan, target, colorReset)
		},
		dryRunChecks: func(_ string, result migration.OperatorScanResult) error {
			if result.Status == migration.OperatorStatusEligible {
				printCheckResults(result.Checks)
			} else {
				printCheckResults(result.FailedChecks)
			}
			return nil
		},
		dryRunPreview: printDryRunPreview,
		dryRunDone:    func(string) error { return nil },
		convertStart: func(target string) {
			fmt.Printf("\n%s%s🔄 Migrating %s to OLMv1...%s\n", colorBold, colorCyan, target, colorReset)
		},
		convertDone: func(_, name string) error {
			banner(fmt.Sprintf("Migration complete! %s is now managed by OLMv1", name))
			fmt.Println()
			return nil
		},
		noTargets: func(_, message string) error { info(message + "."); return nil },
		batchStart: func(command string, count int) {
			switch command {
			case "rollback":
				fmt.Printf("\nRolling back %d migrated ClusterExtension(s)...\n", count)
			case "cleanup":
				fmt.Printf("\nCleaning up %d Conflict-state ClusterExtension(s)...\n", count)
			}
		},
		batchProcessing: func(target string) { info(fmt.Sprintf("Processing %s...", target)) },
		batchResult: func(command, target string, resultErr error) error {
			if command == "convert" {
				if resultErr != nil {
					fail(fmt.Sprintf("%s: %v", target, resultErr))
				}
				return nil // successful conversion already reports progress
			}
			if resultErr != nil {
				fail(fmt.Sprintf("%s: %v", target, resultErr))
			} else if command == "rollback" {
				success(fmt.Sprintf("%s rolled back", target))
			} else {
				success(fmt.Sprintf("%s conflict resolved", target))
			}
			return nil
		},
		singleStart: func(command, target string) {
			if command == "rollback" {
				fmt.Printf("\n%s%s🔄 Rolling back ClusterExtension %s...%s\n", colorBold, colorCyan, target, colorReset)
			} else {
				fmt.Printf("\n%s%s🧹 Cleaning up Conflict for %s...%s\n", colorBold, colorCyan, target, colorReset)
			}
		},
		singleResult: func(command, target string, resultErr error) error {
			if resultErr != nil {
				if command == "rollback" {
					fail(fmt.Sprintf("Rollback failed: %v", resultErr))
				} else {
					fail(fmt.Sprintf("Cleanup failed: %v", resultErr))
				}
				return nil
			}
			if command == "rollback" {
				success(fmt.Sprintf("ClusterExtension %s rolled back; Subscription restored", target))
			} else {
				success(fmt.Sprintf("Conflict resolved for %s; OLMv0 artifacts removed, ClusterExtension intact", target))
			}
			fmt.Println()
			return nil
		},
		fatalError: func(err error) { _ = clioutput.WriteError(os.Stdout, os.Stderr, formatter, err) },
	}
	if formatter.Structured() {
		output.scanStart = func(string) {}
		output.scanResults = func(command string, results []migration.OperatorScanResult) error {
			return formatter.WriteRecord(os.Stdout, outputRecord{Type: "scan", Command: command, Data: scanResultsData(results)})
		}
		output.checkStart = func(string) {}
		output.checkResult = func(command, target string, result migration.OperatorScanResult) error {
			return formatter.WriteRecord(os.Stdout, outputRecord{Type: "check", Command: command, Target: target, Data: scanResultData(result)})
		}
		output.dryRunStart = func(string) {}
		output.dryRunChecks = func(target string, result migration.OperatorScanResult) error {
			if result.Status != migration.OperatorStatusEligible {
				return formatter.WriteRecord(os.Stdout, outputRecord{Type: "check", Command: "convert", Target: target, Data: scanResultData(result)})
			}
			return nil
		}
		output.dryRunPreview = func(opts, resourceOpts migration.Options, info *migration.MigrationInfo) error {
			return formatter.WriteRecord(os.Stdout, dryRunRecord(opts, resourceOpts, info))
		}
		output.dryRunDone = func(target string) error {
			return formatter.WriteRecord(os.Stdout, resultRecord("convert", target, nil))
		}
		output.convertStart = func(string) {}
		output.convertDone = func(target, _ string) error {
			return formatter.WriteRecord(os.Stdout, resultRecord("convert", target, nil))
		}
		output.noTargets = func(command, message string) error {
			return formatter.WriteRecord(os.Stdout, outputRecord{Type: "result", Command: command, Status: migration.ProgressCompleted, Message: message})
		}
		output.batchStart = func(string, int) {}
		output.batchProcessing = func(string) {}
		output.batchResult = func(command, target string, resultErr error) error {
			return formatter.WriteRecord(os.Stdout, resultRecord(command, target, resultErr))
		}
		output.singleStart = func(string, string) {}
		output.singleResult = func(command, target string, resultErr error) error {
			if resultErr == nil {
				return formatter.WriteRecord(os.Stdout, resultRecord(command, target, nil))
			}
			return nil // main emits the terminal error record
		}
		output.fatalError = func(err error) { _ = clioutput.WriteError(os.Stdout, os.Stderr, formatter, err) }
	}
	return output
}

func resultRecord(command, target string, resultErr error) outputRecord {
	return clioutput.ResultRecord(command, target, resultErr)
}
