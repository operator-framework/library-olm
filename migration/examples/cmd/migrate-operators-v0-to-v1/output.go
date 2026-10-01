package main

import (
	"fmt"
	"strings"
	"sync"

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

func progressFunc(msg string) {
	progressMu.Lock()
	defer progressMu.Unlock()
	if strings.HasPrefix(msg, "Warning:") || strings.HasPrefix(msg, "Note:") {
		if progressRunning && progressMsg != "" {
			fmt.Printf("\r%80s\r", "")
		}
		progressMsg = ""
		if strings.HasPrefix(msg, "Warning:") {
			warn(strings.TrimSpace(strings.TrimPrefix(msg, "Warning:")))
		} else {
			info(msg)
		}
		return
	}
	progressMsg = msg
	if progressRunning {
		fmt.Printf("\r  %s%s...%s", colorDim, msg, colorReset)
	}
}

func startProgress() {
	progressMu.Lock()
	progressRunning = true
	progressMu.Unlock()
}

func clearProgress() {
	progressMu.Lock()
	progressRunning = false
	if progressMsg != "" {
		fmt.Printf("\r%80s\r", "") // clear the line
	}
	progressMsg = ""
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
