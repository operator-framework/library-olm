// Package clioutput contains format-neutral records and progress rendering for
// both migration CLIs and callers of the migration APIs.
package clioutput

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/operator-framework/library-olm/migration/pkg/migration"
)

const (
	Text  = "text"
	JSONL = "jsonl"
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

// Record is one JSON Lines CLI message. Data is command-specific.
type Record struct {
	Type    string                   `json:"type"`
	Command string                   `json:"command,omitempty"`
	Target  string                   `json:"target,omitempty"`
	Step    migration.ProgressStep   `json:"step,omitempty"`
	Status  migration.ProgressStatus `json:"status,omitempty"`
	Message string                   `json:"message,omitempty"`
	Error   string                   `json:"error,omitempty"`
	Data    any                      `json:"data,omitempty"`
}

// ValidateFormat accepts the text and JSON Lines CLI output modes.
func ValidateFormat(mode string) error {
	if mode != Text && mode != JSONL {
		return fmt.Errorf("invalid --output %q: expected %s or %s", mode, JSONL, Text)
	}
	return nil
}

// WriteJSONLine encodes one record without escaping HTML in messages.
func WriteJSONLine(out io.Writer, record Record) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(record)
}

// ProgressRecord maps an API progress event to the common CLI envelope.
// An event target takes precedence over the caller's single-operation target.
func ProgressRecord(command, target string, event migration.ProgressEvent) Record {
	if event.Target != "" {
		target = event.Target
	}
	record := Record{Type: "progress", Command: command, Target: target, Step: event.Step, Status: event.Status, Message: event.Message}
	if event.Err != nil {
		record.Error = event.Err.Error()
	}
	return record
}

// ResultRecord maps an operation outcome to the common CLI envelope.
func ResultRecord(command, target string, resultErr error) Record {
	record := Record{Type: "result", Command: command, Target: target, Status: migration.ProgressCompleted}
	if resultErr != nil {
		record.Status = migration.ProgressFailed
		record.Error = resultErr.Error()
	}
	return record
}

// ErrorRecord maps a terminal command error to the common CLI envelope.
func ErrorRecord(err error) Record { return Record{Type: "error", Error: err.Error()} }

// WriteTextProgress renders one event, retaining a pending waiting message so
// the next event can clear the same terminal line.
// WriteTextProgress renders a typed progress event in the human-readable style.
func WriteTextProgress(out io.Writer, event migration.ProgressEvent, running bool, pending *string) {
	if *pending != "" {
		fmt.Fprintf(out, "\r%80s\r", "")
		*pending = ""
	}
	switch event.Status {
	case migration.ProgressStarted:
		fmt.Fprintf(out, "\n%s%s%s%s\n", colorBold, colorCyan, event.Message, colorReset)
	case migration.ProgressWaiting:
		if running {
			*pending = event.Message
			fmt.Fprintf(out, "\r  %s%s%s", colorDim, event.Message, colorReset)
		}
	case migration.ProgressCompleted:
		message := event.Message
		if message == "" {
			message = fmt.Sprintf("%s complete", event.Step)
		}
		fmt.Fprintf(out, "  %s✓%s %s\n", colorGreen, colorReset, message)
	case migration.ProgressFailed:
		fmt.Fprintf(out, "  %s✗%s %s\n", colorRed, colorReset, event.Message)
	case migration.ProgressWarning:
		message := event.Message
		if event.Err != nil {
			message = fmt.Sprintf("%s: %v", message, event.Err)
		}
		fmt.Fprintf(out, "  %s⚠%s  %s\n", colorYellow, colorReset, message)
	case migration.ProgressNote:
		fmt.Fprintf(out, "  %s\n", event.Message)
	}
}

// ClearTextProgress clears a waiting message left on the terminal line.
func ClearTextProgress(out io.Writer, pending *string) {
	if *pending != "" {
		fmt.Fprintf(out, "\r%80s\r", "")
	}
	*pending = ""
}

// WriteError writes a terminal error to stdout as JSONL or to stderr as text.
func WriteError(out, errOut io.Writer, mode string, err error) error {
	if mode == JSONL {
		return WriteJSONLine(out, ErrorRecord(err))
	}
	_, writeErr := fmt.Fprintln(errOut, "Error:", err)
	return writeErr
}

// FormatNames returns supported output modes for CLI help text.
func FormatNames() string { return strings.Join([]string{JSONL, Text}, " or ") }
