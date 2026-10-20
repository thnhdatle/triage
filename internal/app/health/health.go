// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thnhdatle/triage/internal/platform/clock"
)

// Defaults of Options (health.*).
const (
	DefaultInterval         = 10 * time.Second
	DefaultFailureThreshold = 3
	DefaultCooldown         = 30 * time.Second
	// MaxProbeTimeout caps the time one probe may take.
	MaxProbeTimeout = 5 * time.Second
)

// Options are the health.* settings. Zero values take the defaults.
type Options struct {
	Interval         time.Duration
	FailureThreshold int
	Cooldown         time.Duration
}

func (o Options) withDefaults() Options {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = DefaultFailureThreshold
	}
	if o.Cooldown <= 0 {
		o.Cooldown = DefaultCooldown
	}
	return o
}

// Check is one active probe and the targets its outcome applies to.
type Check struct {
	Targets []string
	Probe   func(ctx context.Context) error
}

// Option configures a Tracker.
type Option func(*Tracker)

// WithOnChange registers fn, called with a target's health after each probe
// round and whenever a report changes it. fn must not block or call back
// into the Tracker.
func WithOnChange(fn func(target string, healthy bool)) Option {
	return func(t *Tracker) { t.onChange = fn }
}

type state struct {
	failures int
	until    time.Time // unhealthy until this instant
}

// Tracker holds the health of every target. It is safe for concurrent use.
type Tracker struct {
	clk      clock.Clock
	onChange func(string, bool)

	mu     sync.Mutex // guards opts and states
	opts   Options
	states map[string]*state
}

// New returns a Tracker. It starts no goroutine; see Run.
func New(clk clock.Clock, opts Options, o ...Option) (*Tracker, error) {
	if clk == nil {
		return nil, errors.New("health: nil clock")
	}
	t := &Tracker{clk: clk, opts: opts.withDefaults(), states: map[string]*state{}}
	for _, fn := range o {
		fn(t)
	}
	return t, nil
}

// SetOptions replaces the settings (configuration reload). Running
// cooldowns keep their end time.
func (t *Tracker) SetOptions(opts Options) {
	t.mu.Lock()
	t.opts = opts.withDefaults()
	t.mu.Unlock()
}

// IsHealthy implements route.Health. Unknown targets are healthy.
func (t *Tracker) IsHealthy(target string) bool {
	now := t.clk.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.states[target]
	return !ok || !now.Before(s.until)
}

// Report implements dispatch.Health: nil resets the failure count, an error
// counts towards health.failure_threshold.
func (t *Tracker) Report(target string, err error) {
	before := t.IsHealthy(target)
	t.record(target, err)
	if after := t.IsHealthy(target); after != before && t.onChange != nil {
		t.onChange(target, after)
	}
}

func (t *Tracker) record(target string, err error) {
	now := t.clk.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.states[target]
	if !ok {
		s = &state{}
		t.states[target] = s
	}
	if err == nil {
		s.failures = 0
		return
	}
	s.failures++
	if s.failures >= t.opts.FailureThreshold {
		s.failures = 0
		s.until = now.Add(t.opts.Cooldown)
	}
}

func (t *Tracker) interval() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opts.Interval
}

// Run probes the checks returned by source immediately and then every
// health.interval until ctx ends. Probes of one round run concurrently, each
// bounded by min(interval, MaxProbeTimeout). Run returns ctx.Err().
func (t *Tracker) Run(ctx context.Context, source func() []Check) error {
	for {
		t.round(ctx, source())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.clk.After(t.interval()):
		}
	}
}

func (t *Tracker) round(ctx context.Context, checks []Check) {
	timeout := min(t.interval(), MaxProbeTimeout)
	var wg sync.WaitGroup
	for _, c := range checks {
		if c.Probe == nil {
			continue
		}
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			err := c.Probe(pctx)
			cancel()
			if ctx.Err() != nil {
				return // shutting down: not the target's fault
			}
			for _, name := range c.Targets {
				t.record(name, err)
			}
		})
	}
	wg.Wait()
	if t.onChange == nil {
		return
	}
	for _, c := range checks {
		for _, name := range c.Targets {
			t.onChange(name, t.IsHealthy(name))
		}
	}
}
