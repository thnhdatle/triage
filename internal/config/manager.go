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

const DefaultWatchInterval = time.Second

type ReloadEvent struct {
	Old *Config
	New *Config
	Err error
}

type Option func(*Manager)

func WithOnReload(fn func(ReloadEvent)) Option {
	return func(m *Manager) { m.onReload = fn }
}

func WithCheck(fn func(*Config) error) Option {
	return func(m *Manager) { m.check = fn }
}

func WithWatchInterval(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.interval = d
		}
	}
}

// Manager holds the live configuration and reloads it.
type Manager struct {
	path     string
	cur      atomic.Pointer[Config]
	interval time.Duration
	onReload func(ReloadEvent)
	check    func(*Config) error

	mu       sync.Mutex
	lastHash [sha256.Size]byte
}

// NewManager loads and validates the file at path.
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

func (m *Manager) Path() string { return m.path }

func (m *Manager) Current() *Config { return m.cur.Load() }

// Reload re-reads the file and swaps in the new configuration if it is valid.
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

// Run polls the file and reloads it when its content changes, until ctx is canceled.
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
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if bytes.Equal(m.lastHash[:], hashOf(data)) {
		return
	}
	_ = m.reloadLocked(data)
}

func hashOf(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}
