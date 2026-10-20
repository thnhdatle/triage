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

// StreamFailure is the policy for a stream that fails after bytes were relayed
// to the client (config dispatch.stream_failure).
type StreamFailure string

// Stream failure policies.
const (
	// StreamFailureContinue sends the next eligible target the original
	// messages, the text produced so far and ContinuationInstruction, and
	// relays its output into the same client stream.
	StreamFailureContinue StreamFailure = "continue"
	// StreamFailureError emits an SSE error event and ends the stream.
	StreamFailureError StreamFailure = "error"
)

// ContinuationInstruction is the user message appended after the partial
// assistant message of a continuation attempt. It contains no
// request content, so it never needs masking.
const ContinuationInstruction = "Your previous answer was interrupted. Continue it exactly where it stopped, " +
	"without repeating text that was already written and without any preamble."

// Defaults of Options (dispatch.*).
const (
	DefaultMaxAttempts    = 3
	DefaultAttemptTimeout = 60 * time.Second
	DefaultTotalTimeout   = 120 * time.Second
	DefaultBackoffBase    = 100 * time.Millisecond
	DefaultBackoffMax     = 2 * time.Second
	DefaultMaxRetryAfter  = 10 * time.Second
)

// Backoff is the jittered exponential backoff applied before an attempt on a
// target that was already tried in the same request.
type Backoff struct {
	// Base is the delay before the first retry of a target.
	Base time.Duration
	// Max caps the delay.
	Max time.Duration
}

// Health receives the outcome of every attempt sent to a target, for passive
// health. err is nil on success and on answers that prove the
// upstream is alive (bad request, context length); it is the attempt error on
// retryable failures (rate limit, unavailable, timeout, 5xx, cut stream).
// Attempts ended by the caller's context or the total timeout are not
// reported. Implementations must be safe for concurrent use and must not block.
type Health interface {
	Report(target string, err error)
}

// Options configures a Dispatcher. Zero fields take the defaults above;
// negative durations or counts are rejected by New.
type Options struct {
	// MaxAttempts bounds the attempts sent upstream per request.
	MaxAttempts int
	// AttemptTimeout bounds one attempt: the whole call when non-streaming,
	// the time to the first chunk and between chunks when streaming.
	// model.Target.AttemptTimeout overrides it per target.
	AttemptTimeout time.Duration
	// TotalTimeout bounds the whole request including backoff waits.
	TotalTimeout time.Duration
	// Backoff is the retry backoff.
	Backoff Backoff
	// MaxRetryAfter caps the honoured upstream Retry-After; a larger value
	// removes that target from the rest of the request.
	MaxRetryAfter time.Duration
	// StreamFailure is the policy after bytes were relayed; default continue.
	StreamFailure StreamFailure
	// ContextLengthFallback retries an ErrContextLength once on the first
	// target of Plan.ContextLengthPool with a larger context window.
	ContextLengthFallback bool
	// Health receives attempt outcomes; nil disables reporting.
	Health Health
	// Rand drives the backoff jitter; nil seeds a generator from the clock.
	// The Dispatcher serialises its use.
	Rand *rand.Rand
}

// Plan is what route hands to dispatch for one request.
type Plan struct {
	// Request is the original, unmasked request; Model is the route name.
	Request chat.Request
	// Chain is the ordered list of candidate targets, primary first, already
	// filtered by route (locality, capability, context window, health).
	Chain []model.Target
	// Localities are the allowed localities. Dispatch re-checks every attempt
	// and never sends outside them.
	Localities model.LocalitySet
	// Mapping is non-nil under mask mode with maskable findings; attempts to
	// cloud targets receive Mapping.MaskRequest of their request.
	Mapping *privacy.Mapping
	// Detectors are set under mask mode only; a continuation sent to a cloud
	// target re-scans the partial assistant text with them.
	Detectors []privacy.Detector
	// ContextLengthPool lists the targets eligible for the context-length
	// fallback (Options.ContextLengthFallback); may be nil.
	ContextLengthPool []model.Target
}

// Result is the outcome of a dispatch. On failure it still carries every
// attempt, the summed usage and cost, so the decision can record them.
type Result struct {
	// Response is the final response of Complete, unmasked when its attempt
	// was masked, with Usage summed over all attempts. Zero for Stream.
	Response chat.Response
	// Target is the target of the final attempt.
	Target model.Target
	// Model is the upstream model name the client sees: the final answer's
	// model for Complete, the first relayed attempt's model for Stream.
	Model string
	// Attempts lists every attempt in order, including refused ones.
	Attempts []decision.Attempt
	// Usage sums the usage of all attempts.
	Usage chat.Usage
	// CostUSD sums the cost of all attempts.
	CostUSD float64
	// Unmask is ok when masked output reached the client fully restored,
	// partial when a placeholder-like token could not be restored, none when
	// no masked output reached the client.
	Unmask decision.UnmaskStatus
}

// Sink receives the relayed stream. Methods are called from the goroutine
// that called Stream.
type Sink interface {
	// Start is called exactly once, before the first relayed byte, with the
	// target of that attempt (header X-Triage-Target).
	Start(target model.Target) error
	// Chunk relays one chunk.
	Chunk(c chat.Chunk) error
	// Comment writes an SSE comment line, e.g. "triage-unmask=partial".
	Comment(text string) error
	// Error emits an SSE error event (stream_failure error, or no attempt
	// could finish the stream after bytes were sent).
	Error(err error) error
}

// Dispatcher runs the fallback chain of requests. It is safe for concurrent
// use.
type Dispatcher struct {
	providers map[string]provider.Provider
	clk       clock.Clock
	opts      Options

	mu  sync.Mutex // protects rng
	rng *rand.Rand
}

// New returns a Dispatcher for providers keyed by model.Target.Provider.
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

// backoff returns the jittered delay before retry n (n >= 1) of one target:
// equal jitter over min(Max, Base*2^(n-1)), i.e. uniform in [d/2, d].
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
