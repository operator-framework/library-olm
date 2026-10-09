// Package clioutput contains format-neutral records and progress rendering for
// both migration CLIs and callers of the migration APIs.
package clioutput

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
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

// Formatter serializes structured records. Text presentation is handled by
// each command; a text formatter therefore does not accept records.
type Formatter interface {
	Structured() bool
	WriteRecord(io.Writer, Record) error
}

type textFormatter struct{}

func (textFormatter) Structured() bool { return false }
func (textFormatter) WriteRecord(io.Writer, Record) error {
	return fmt.Errorf("text output does not accept structured records")
}

type jsonLinesFormatter struct{}

func (jsonLinesFormatter) Structured() bool { return true }
func (jsonLinesFormatter) WriteRecord(out io.Writer, record Record) error {
	return WriteJSONLine(out, record)
}

// Registry holds available CLI formats. Both migration commands use registries
// initialized here, so adding a built-in format supports both command flows.
type Registry struct {
	formats map[string]Formatter
}

// NewRegistry returns the built-in text and JSON Lines formats.
func NewRegistry() *Registry {
	return &Registry{formats: map[string]Formatter{Text: textFormatter{}, JSONL: jsonLinesFormatter{}}}
}

// Register adds or replaces a named formatter.
func (r *Registry) Register(name string, formatter Formatter) {
	r.formats[name] = formatter
}

// Names returns format names in stable order for CLI help and errors.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.formats))
	for name := range r.formats {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate reports an unsupported output format.
func (r *Registry) Validate(name string) error {
	if _, ok := r.formats[name]; !ok {
		return fmt.Errorf("invalid --output %q: expected %s", name, strings.Join(r.Names(), " or "))
	}
	return nil
}

// Select returns the named formatter, falling back to text for validation
// errors so an invalid format never produces a misleading structured record.
func (r *Registry) Select(name string) Formatter {
	if formatter, ok := r.formats[name]; ok {
		return formatter
	}
	return r.formats[Text]
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

// WriteTextProgress renders a typed progress event, retaining a pending waiting
// message so the next event can clear the same terminal line.
func WriteTextProgress(out io.Writer, event migration.ProgressEvent, running bool, pending *string) error {
	if *pending != "" {
		if _, err := fmt.Fprintf(out, "\r%80s\r", ""); err != nil {
			return err
		}
		*pending = ""
	}
	switch event.Status {
	case migration.ProgressStarted:
		_, err := fmt.Fprintf(out, "\n%s%s%s%s\n", colorBold, colorCyan, event.Message, colorReset)
		return err
	case migration.ProgressWaiting:
		if running {
			*pending = event.Message
			_, err := fmt.Fprintf(out, "\r  %s%s%s", colorDim, event.Message, colorReset)
			return err
		}
	case migration.ProgressCompleted:
		message := event.Message
		if message == "" {
			message = fmt.Sprintf("%s complete", event.Step)
		}
		_, err := fmt.Fprintf(out, "  %s✓%s %s\n", colorGreen, colorReset, message)
		return err
	case migration.ProgressFailed:
		_, err := fmt.Fprintf(out, "  %s✗%s %s\n", colorRed, colorReset, event.Message)
		return err
	case migration.ProgressWarning:
		message := event.Message
		if event.Err != nil {
			message = fmt.Sprintf("%s: %v", message, event.Err)
		}
		_, err := fmt.Fprintf(out, "  %s⚠%s  %s\n", colorYellow, colorReset, message)
		return err
	case migration.ProgressNote:
		_, err := fmt.Fprintf(out, "  %s\n", event.Message)
		return err
	}
	return nil
}

// ClearTextProgress clears a waiting message left on the terminal line.
func ClearTextProgress(out io.Writer, pending *string) error {
	var err error
	if *pending != "" {
		_, err = fmt.Fprintf(out, "\r%80s\r", "")
	}
	*pending = ""
	return err
}

// WriteError writes a terminal error using the selected formatter, or to
// stderr for text output.
func WriteError(out, errOut io.Writer, formatter Formatter, err error) error {
	if formatter.Structured() {
		return formatter.WriteRecord(out, ErrorRecord(err))
	}
	_, writeErr := fmt.Fprintln(errOut, "Error:", err)
	return writeErr
}
