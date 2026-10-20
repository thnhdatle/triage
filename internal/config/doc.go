// SPDX-License-Identifier: Apache-2.0

// Package config loads, validates and hot-reloads the configuration.
//
// Parse decodes strict YAML (unknown keys are rejected) over Default, Validate
// collects every problem into one *ValidationError, and Load does both for a
// file. Durations accept the Go syntax plus a "d" day suffix ("30s", "14d");
// sizes are bytes with an optional suffix ("8MiB"). A Config is immutable once
// loaded.
//
// Manager holds the live Config in an atomic.Pointer: Current is lock-free,
// Reload (wired to SIGHUP and the admin endpoint) swaps in a new valid Config
// and keeps the old one on error, and Run polls the file and reloads when its
// content changes. Every option is documented in deploy/config.example.yaml,
// which a test keeps in sync with the structs.
//
//	m, err := config.NewManager("triage.yaml")
//	cfg := m.Current()
//	_ = m.Reload()
package config
