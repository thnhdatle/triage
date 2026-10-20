// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/thnhdatle/triage/acceptance"
)

const (
	logsDir        = "acceptance/logs"
	composeFile    = "deploy/docker-compose.yml"
	criterionLimit = 90 * time.Minute
)

// runCmd runs a command, streaming combined output to w.
func runCmd(timeout time.Duration, w io.Writer, extraEnv []string, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // commands are built by the runner
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.Env = append(os.Environ(), extraEnv...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("`%s %s`: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// goTestArgs returns the go test arguments for a criterion's test name.
// The go test timeout matches the runner's per-criterion limit (go test's default is 10 min,
// shorter than several criteria, e.g. TRG-13's three 200 s open-model runs).
func goTestArgs(name string) []string {
	base := []string{"test", "-tags", "acceptance", "-count=1", "-timeout", criterionLimit.String()}
	if strings.HasPrefix(name, "Benchmark") {
		return append(base, "-run", "^$", "-bench", "^"+name+"$", "-benchtime", "1x", "./acceptance/...")
	}
	return append(base, "-run", "^"+name+"$", "-json", "./acceptance/...")
}

func logPath(id, ext string) string { return filepath.Join(logsDir, id+"."+ext) }

// readResult reads acceptance/logs/<ID>.json.
func readResult(id string) (measured map[string]any, pass bool, details string, err error) {
	data, err := os.ReadFile(logPath(id, "json"))
	if err != nil {
		return nil, false, "", fmt.Errorf("read result: %w", err)
	}
	var r struct {
		Measured map[string]any `json:"measured"`
		Pass     bool           `json:"pass"`
		Details  string         `json:"details"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, false, "", fmt.Errorf("parse result: %w", err)
	}
	return r.Measured, r.Pass, r.Details, nil
}

// execute runs one criterion's test and builds its row.
func execute(c *acceptance.Criterion, env string, thr map[string]any) Row {
	row := Row{ID: c.ID, Threshold: formatMap(thr)}
	_ = os.MkdirAll(logsDir, 0o750)
	_ = os.Remove(logPath(c.ID, "json"))
	f, err := os.Create(logPath(c.ID, "txt")) //nolint:gosec // path built from criterion id
	if err != nil {
		row.Status, row.Measured = StatusFail, "cannot create log: "+err.Error()
		return row
	}
	defer func() { _ = f.Close() }()
	runErr := runCmd(criterionLimit, f, []string{"TRIAGE_ACCEPTANCE_ENV=" + env}, "go", goTestArgs(c.Test)...)
	measured, pass, details, readErr := readResult(c.ID)
	row.Measured = formatMap(measured)
	switch {
	case readErr != nil:
		row.Status, row.Measured = StatusFail, "no result: "+readErr.Error()
	case runErr == nil && pass:
		row.Status = StatusPass
	default:
		row.Status = StatusFail
		if details != "" {
			row.Measured += " (" + details + ")"
		}
	}
	return row
}

// say writes a progress message; write errors are irrelevant for console output.
func say(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}
