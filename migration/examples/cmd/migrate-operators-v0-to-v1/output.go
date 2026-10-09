package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/operator-framework/library-olm/migration/pkg/clioutput"
	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
)

var (
	progressMu       sync.Mutex
	progressRunning  bool
	progressMsg      string
	progressWriteErr error
)

type outputRecord = clioutput.Record

type scanResultRecord struct {
	Namespace string                   `json:"namespace"`
	Name      string                   `json:"name"`
	Package   string                   `json:"package,omitempty"`
	Version   string                   `json:"version,omitempty"`
	Status    migration.OperatorStatus `json:"status"`
	Reason    string                   `json:"reason,omitempty"`
	Checks    []checkResultRecord      `json:"checks,omitempty"`
	Warnings  []string                 `json:"warnings,omitempty"`
	Error     string                   `json:"error,omitempty"`
}

type checkResultRecord struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

var outputFormats = clioutput.NewRegistry()

func selectedFormatter() clioutput.Formatter { return outputFormats.Select(outputMode) }

func scanResultData(result migration.OperatorScanResult) scanResultRecord {
	data := scanResultRecord{
		Namespace: result.SubscriptionNamespace,
		Name:      result.SubscriptionName,
		Package:   result.PackageName,
		Version:   result.Version,
		Status:    result.Status,
		Reason:    result.Reason,
		Warnings:  result.Warnings,
	}
	for _, check := range result.Checks {
		data.Checks = append(data.Checks, checkResultRecord{Name: check.Name, Passed: check.Passed, Message: check.Message})
	}
	if result.Error != nil {
		data.Error = result.Error.Error()
	}
	return data
}

func scanResultsData(results []migration.OperatorScanResult) []scanResultRecord {
	data := make([]scanResultRecord, 0, len(results))
	for _, result := range results {
		data = append(data, scanResultData(result))
	}
	return data
}

func structuredOutput() bool { return selectedFormatter().Structured() }

func writeOutputRecord(record outputRecord) error {
	return selectedFormatter().WriteRecord(os.Stdout, record)
}

func progressFunc(event migration.ProgressEvent) {
	progressFuncWithContext(event, "", "")
}

func progressFuncFor(command, target string) migration.ProgressFunc {
	return func(event migration.ProgressEvent) { progressFuncWithContext(event, command, target) }
}

func progressFuncWithContext(event migration.ProgressEvent, command, target string) {
	progressMu.Lock()
	defer progressMu.Unlock()
	var err error
	if structuredOutput() {
		err = writeOutputRecord(clioutput.ProgressRecord(command, target, event))
	} else {
		err = clioutput.WriteTextProgress(os.Stdout, event, progressRunning, &progressMsg)
	}
	if err != nil && progressWriteErr == nil {
		progressWriteErr = fmt.Errorf("write operator progress: %w", err)
	}
}

func startProgress() {
	progressMu.Lock()
	progressRunning = true
	progressWriteErr = nil
	progressMu.Unlock()
}

func progressError() error {
	progressMu.Lock()
	defer progressMu.Unlock()
	return progressWriteErr
}

func clearProgress() {
	progressMu.Lock()
	progressRunning = false
	if structuredOutput() {
		progressMsg = ""
		progressMu.Unlock()
		return
	}
	if err := clioutput.ClearTextProgress(os.Stdout, &progressMsg); err != nil && progressWriteErr == nil {
		progressWriteErr = fmt.Errorf("clear operator progress: %w", err)
	}
	progressMu.Unlock()
}

func sectionHeader(title string) {
	fmt.Printf("\n  %s%s%s\n", colorBold, title, colorReset)
}

func banner(msg string) {
	fmt.Printf("\n%s%s✅ %s%s\n", colorBold, colorGreen, msg, colorReset)
}

func success(msg string) {
	fmt.Printf("  %s✓%s %s\n", colorGreen, colorReset, msg)
}

func fail(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

func warn(msg string) {
	fmt.Printf("  %s⚠%s  %s\n", colorYellow, colorReset, msg)
}

func info(msg string) {
	fmt.Printf("  %s\n", msg)
}

func detail(key, value string) {
	fmt.Printf("    %s%-22s%s %s\n", colorDim, key, colorReset, value)
}

func printCheckResults(checks []migration.CheckResult) {
	for _, c := range checks {
		if c.Passed {
			success(fmt.Sprintf("%-35s %s%s%s", c.Name, colorDim, c.Message, colorReset))
		} else {
			fail(fmt.Sprintf("%-35s %s", c.Name, c.Message))
		}
	}
}
