// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	criteriaPath = "criteria.yaml"
	logsDir      = "logs"
	envVar       = "TRIAGE_ACCEPTANCE_ENV"
)

// Result is the content of acceptance/logs/<ID>.json.
type Result struct {
	ID       string         `json:"id"`
	Measured map[string]any `json:"measured"`
	Pass     bool           `json:"pass"`
	Details  string         `json:"details"`
}

// acceptanceEnv returns the threshold environment selected by the runner.
func acceptanceEnv(tb testing.TB) string {
	tb.Helper()
	env := os.Getenv(envVar)
	if env == "" {
		env = EnvMac
	}
	if env != EnvMac && env != EnvCI {
		tb.Fatalf("%s must be %q or %q, got %q", envVar, EnvMac, EnvCI, env)
	}
	return env
}

// thresholds loads the criterion's thresholds for the current environment at runtime.
func thresholds(tb testing.TB, id string) map[string]any {
	tb.Helper()
	f, err := Load(criteriaPath)
	if err != nil {
		tb.Fatalf("load criteria: %v", err)
	}
	c, ok := f.Find(id)
	if !ok {
		tb.Fatalf("criterion %s not in %s", id, criteriaPath)
	}
	vals, skip := c.Thresholds(acceptanceEnv(tb))
	if skip {
		tb.Fatalf("criterion %s has no thresholds for %s; the runner must not execute it", id, acceptanceEnv(tb))
	}
	return vals
}

// writeResult writes acceptance/logs/<ID>.json.
func writeResult(tb testing.TB, r Result) {
	tb.Helper()
	if r.Measured == nil {
		r.Measured = map[string]any{}
	}
	if err := os.MkdirAll(logsDir, 0o750); err != nil {
		tb.Fatalf("create logs dir: %v", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		tb.Fatalf("encode result: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, r.ID+".json"), append(data, '\n'), 0o600); err != nil {
		tb.Fatalf("write result: %v", err)
	}
}
