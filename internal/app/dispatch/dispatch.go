// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

type StreamFailure string

const (
	StreamFailureContinue StreamFailure = "continue"
	StreamFailureError    StreamFailure = "error"
)

const ContinuationInstruction = "Your previous answer was interrupted. Continue it exactly where it stopped, " +
	"without repeating text that was already written and without any preamble."

const (
	DefaultMaxAttempts    = 3
	DefaultAttemptTimeout = 60 * time.Second
	DefaultTotalTimeout   = 120 * time.Second
	DefaultBackoffBase    = 100 * time.Millisecond
	DefaultBackoffMax     = 2 * time.Second
	DefaultMaxRetryAfter  = 10 * time.Second
)

type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

type Health interface {
	Report(target string, err error)
}

type Options struct {
	MaxAttempts           int
	AttemptTimeout        time.Duration
	TotalTimeout          time.Duration
	Backoff               Backoff
	MaxRetryAfter         time.Duration
	StreamFailure         StreamFailure
	ContextLengthFallback bool
	Health                Health
	Rand                  *rand.Rand
}

type Plan struct {
	Request           chat.Request
	Chain             []model.Target
	Localities        model.LocalitySet
	Mapping           *privacy.Mapping
	Detectors         []privacy.Detector
	ContextLengthPool []model.Target
}

type Result struct {
	Response chat.Response
	Target   model.Target
	Model    string
	Attempts []decision.Attempt
	Usage    chat.Usage
	CostUSD  float64
	Unmask   decision.UnmaskStatus
}

type Sink interface {
	Start(target model.Target) error
	Chunk(c chat.Chunk) error
	Comment(text string) error
	Error(err error) error
}

// Dispatcher runs the fallback chain of a routed request.
type Dispatcher struct {
	providers map[string]provider.Provider
	clk       clock.Clock
	opts      Options

	mu  sync.Mutex
	rng *rand.Rand
}

// New returns a Dispatcher for the given providers.
func New(providers map[string]provider.Provider, clk clock.Clock, opts Options) (*Dispatcher, error) {
	if clk == nil {
		return nil, errors.New("dispatch: nil clock")
	}
	opts, err := withDefaults(opts)
	if err != nil {
		return nil, err
	}
	ps := make(map[string]provider.Provider, len(providers))
	for name, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("dispatch: provider %q is nil", name)
		}
		ps[name] = p
	}
	rng := opts.Rand
	if rng == nil {
		seed := uint64(clk.Now().UnixNano())                       //nolint:gosec // any bit pattern is a valid seed
		rng = rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // jitter, not security
	}
	return &Dispatcher{providers: ps, clk: clk, opts: opts, rng: rng}, nil
}

func withDefaults(o Options) (Options, error) {
	if o.MaxAttempts < 0 || o.AttemptTimeout < 0 || o.TotalTimeout < 0 || o.Backoff.Base < 0 ||
		o.Backoff.Max < 0 || o.MaxRetryAfter < 0 {
		return o, errors.New("dispatch: negative option")
	}
	o.MaxAttempts = orDefault(o.MaxAttempts, DefaultMaxAttempts)
	o.AttemptTimeout = orDefault(o.AttemptTimeout, DefaultAttemptTimeout)
	o.TotalTimeout = orDefault(o.TotalTimeout, DefaultTotalTimeout)
	o.Backoff.Base = orDefault(o.Backoff.Base, DefaultBackoffBase)
	o.Backoff.Max = orDefault(o.Backoff.Max, DefaultBackoffMax)
	o.MaxRetryAfter = orDefault(o.MaxRetryAfter, DefaultMaxRetryAfter)
	if o.Backoff.Max < o.Backoff.Base {
		return o, errors.New("dispatch: backoff max below base")
	}
	switch o.StreamFailure {
	case "":
		o.StreamFailure = StreamFailureContinue
	case StreamFailureContinue, StreamFailureError:
	default:
		return o, fmt.Errorf("dispatch: unknown stream_failure %q (want continue or error)", o.StreamFailure)
	}
	return o, nil
}

func orDefault[T int | time.Duration](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}

func (d *Dispatcher) backoff(n int) time.Duration {
	delay := d.opts.Backoff.Max
	if n-1 < 32 {
		if b := d.opts.Backoff.Base << (n - 1); b > 0 && b < delay {
			delay = b
		}
	}
	half := delay / 2
	d.mu.Lock()
	j := time.Duration(d.rng.Int64N(int64(delay-half) + 1))
	d.mu.Unlock()
	return half + j
}

func (d *Dispatcher) attemptTimeout(t model.Target) time.Duration {
	if t.AttemptTimeout > 0 {
		return t.AttemptTimeout
	}
	return d.opts.AttemptTimeout
}
