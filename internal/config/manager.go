// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultWatchInterval is how often Run polls the config file.
const DefaultWatchInterval = time.Second

// ReloadEvent describes one reload attempt. Old is the Config in effect before
// the attempt; New is nil when Err is set (the old Config stays in effect).
type ReloadEvent struct {
	Old *Config
	New *Config
	Err error
}

// Option configures a Manager.
type Option func(*Manager)

// WithOnReload registers a callback invoked after every reload attempt, after
// the swap. Calls are serialised; the callback must not call Reload.
func WithOnReload(fn func(ReloadEvent)) Option {
	return func(m *Manager) { m.onReload = fn }
}

// WithCheck registers an extra check run on every valid Config before it is
// swapped in, at NewManager and on every reload (e.g. artifact presence, which
// Validate does not check because it never touches the filesystem). An error
// rejects the Config exactly like a validation error: the old one stays in
// effect. Calls are serialised with reloads.
func WithCheck(fn func(*Config) error) Option {
	return func(m *Manager) { m.check = fn }
}

// WithWatchInterval sets the polling interval of Run (default one second).
func WithWatchInterval(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.interval = d
		}
	}
}

// Manager owns the live configuration. Current is lock-free; in-flight
// requests keep the *Config they started with, so a swap never changes a
// request midway.
type Manager struct {
	path     string
	cur      atomic.Pointer[Config]
	interval time.Duration
	onReload func(ReloadEvent)
	check    func(*Config) error

	mu       sync.Mutex // serialises Reload and guards the fields below
	lastHash [sha256.Size]byte
}

// NewManager loads and validates the file at path. It starts no goroutines.
func NewManager(path string, opts ...Option) (*Manager, error) {
	m := &Manager{path: path, interval: DefaultWatchInterval}
	for _, o := range opts {
		o(m)
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-chosen config path
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg, err := m.load(path, data)
	if err != nil {
		return nil, err
	}
	m.lastHash = sha256.Sum256(data)
	m.cur.Store(cfg)
	return m, nil
}

// Path returns the config file path.
func (m *Manager) Path() string { return m.path }

// Current returns the live configuration. Callers must not modify it.
func (m *Manager) Current() *Config { return m.cur.Load() }

// Reload re-reads the file, and on success atomically swaps it in. An invalid
// or unreadable file leaves the old configuration in effect and returns the
// error (also reported to the WithOnReload callback). It is safe to call
// concurrently, e.g. from a SIGHUP handler and the admin endpoint.
func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.path) //nolint:gosec // operator-chosen config path
	if err != nil {
		err = fmt.Errorf("config: read %s: %w", m.path, err)
		m.report(ReloadEvent{Old: m.cur.Load(), Err: err})
		return err
	}
	return m.reloadLocked(data)
}

func (m *Manager) reloadLocked(data []byte) error {
	old := m.cur.Load()
	m.lastHash = sha256.Sum256(data)
	cfg, err := m.load(m.path, data)
	if err != nil {
		m.report(ReloadEvent{Old: old, Err: err})
		return err
	}
	m.cur.Store(cfg)
	m.report(ReloadEvent{Old: old, New: cfg})
	return nil
}

// load parses, validates and runs the WithCheck hook.
func (m *Manager) load(path string, data []byte) (*Config, error) {
	cfg, err := loadBytes(path, data)
	if err != nil {
		return nil, err
	}
	if m.check != nil {
		if err := m.check(cfg); err != nil {
			return nil, fmt.Errorf("load %s: %w", path, err)
		}
	}
	return cfg, nil
}

func (m *Manager) report(ev ReloadEvent) {
	if m.onReload != nil {
		m.onReload(ev)
	}
}

// Run polls the file every watch interval and reloads when its content
// changes, until ctx is cancelled. A changed-but-invalid file is reported once
// and not retried until it changes again. Run always returns ctx.Err().
func (m *Manager) Run(ctx context.Context) error {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			m.poll()
		}
	}
}

func (m *Manager) poll() {
	data, err := os.ReadFile(m.path) //nolint:gosec // operator-chosen config path
	if err != nil {
		// Editors replace files non-atomically; wait for the next tick.
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if bytes.Equal(m.lastHash[:], hashOf(data)) {
		return
	}
	_ = m.reloadLocked(data) // the error is delivered through the callback
}

func hashOf(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}
