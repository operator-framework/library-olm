package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
	progressMu      sync.Mutex
	progressRunning bool
	progressMsg     string
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

// outputFormatter owns record serialization. Command presentation is selected
// separately in selectedCommandOutput; another structured format can be
// registered here without changing the command flows.
type outputFormatter interface {
	structured() bool
	writeRecord(outputRecord) error
}

type textFormatter struct{}

func (textFormatter) structured() bool { return false }

func (textFormatter) writeRecord(outputRecord) error {
	return fmt.Errorf("text output does not accept structured records")
}

type jsonLinesFormatter struct{}

func (jsonLinesFormatter) structured() bool { return true }

func (jsonLinesFormatter) writeRecord(record outputRecord) error {
	return clioutput.WriteJSONLine(os.Stdout, record)
}

var outputFormatters = map[string]outputFormatter{
	"text":  textFormatter{},
	"jsonl": jsonLinesFormatter{},
}

func outputFormatNames() []string {
	names := make([]string, 0, len(outputFormatters))
	for name := range outputFormatters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateOutputFormat() error {
	if _, ok := outputFormatters[outputMode]; !ok {
		return fmt.Errorf("invalid --output %q: expected %s", outputMode, strings.Join(outputFormatNames(), " or "))
	}
	return nil
}

func selectedFormatter() outputFormatter {
	if formatter, ok := outputFormatters[outputMode]; ok {
		return formatter
	}
	// Cobra reports an invalid format before a command runs. Keep its error in
	// plain text rather than silently selecting a structured format.
	return outputFormatters["text"]
}

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

func structuredOutput() bool { return selectedFormatter().structured() }

func writeOutputRecord(record outputRecord) error {
	return selectedFormatter().writeRecord(record)
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
	if structuredOutput() {
		_ = writeOutputRecord(clioutput.ProgressRecord(command, target, event))
		return
	}
	clioutput.WriteTextProgress(os.Stdout, event, progressRunning, &progressMsg)
}

func startProgress() {
	progressMu.Lock()
	progressRunning = true
	progressMu.Unlock()
}

func clearProgress() {
	progressMu.Lock()
	progressRunning = false
	if structuredOutput() {
		progressMsg = ""
		progressMu.Unlock()
		return
	}
	clioutput.ClearTextProgress(os.Stdout, &progressMsg)
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
