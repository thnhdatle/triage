// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

// run is the state of one dispatch. It is owned by the goroutine that called
// Complete or Stream; nothing in it is shared.
type run struct {
	d    *Dispatcher
	plan Plan
	// chain is plan.Chain restricted to plan.Localities.
	chain []model.Target
	// mapping is plan.Mapping, or a mapping created for a continuation rescan
	// when the request had no findings but detectors are set.
	mapping *privacy.Mapping
	res     Result

	sent      int
	cursor    int
	tries     map[string]int
	notBefore map[string]time.Time
	excluded  map[string]bool
	// cloudBlocked skips cloud targets after a mask leak or a failed rescan.
	cloudBlocked    bool
	ctxFallbackUsed bool
	lastErr         error
	lastTarget      model.Target
}

func (d *Dispatcher) newRun(plan Plan) *run {
	r := &run{
		d:         d,
		plan:      plan,
		mapping:   plan.Mapping,
		tries:     map[string]int{},
		notBefore: map[string]time.Time{},
		excluded:  map[string]bool{},
	}
	for _, t := range plan.Chain {
		if plan.Localities.Allows(t.Locality) {
			r.chain = append(r.chain, t)
		}
	}
	return r
}

// watch returns a context cancelled with cause after d on clk, and a stop
// function that ends the watchdog goroutine, waits for it and releases the
// context. The timer is armed before watch returns.
func watch(parent context.Context, clk clock.Clock, d time.Duration, cause error) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := clk.After(d)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-timer:
			cancel(cause)
		case <-done:
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		close(done)
		wg.Wait()
		cancel(nil)
	}
}

// ctxErr returns why ctx is done (ErrTotalTimeout, the caller's cancellation),
// or nil.
func ctxErr(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	return context.Cause(ctx)
}

// pick returns the target of the next attempt, after waiting for its backoff
// and Retry-After. It returns false when the dispatch must stop.
func (r *run) pick(ctx context.Context) (model.Target, bool) {
	if r.sent >= r.d.opts.MaxAttempts || ctx.Err() != nil {
		return model.Target{}, false
	}
	var (
		t  model.Target
		ok bool
	)
	switch {
	case r.lastErr == nil:
		t, ok = r.nextInChain()
	case errors.Is(r.lastErr, provider.ErrContextLength):
		t, ok = r.largerContext()
	case retryable(r.lastErr):
		t, ok = r.nextInChain()
	}
	if !ok {
		return model.Target{}, false
	}
	if err := r.wait(ctx, t); err != nil {
		return model.Target{}, false
	}
	return t, true
}

// nextInChain returns the next usable chain entry, cycling back to the start
// of the chain, so a short chain still gets MaxAttempts attempts.
func (r *run) nextInChain() (model.Target, bool) {
	n := len(r.chain)
	for i := range n {
		t := r.chain[(r.cursor+i)%n]
		if r.usable(t) {
			r.cursor = (r.cursor + i + 1) % n
			return t, true
		}
	}
	return model.Target{}, false
}

// largerContext returns the context-length fallback target, at most once.
func (r *run) largerContext() (model.Target, bool) {
	if !r.d.opts.ContextLengthFallback || r.ctxFallbackUsed {
		return model.Target{}, false
	}
	r.ctxFallbackUsed = true
	for _, t := range r.plan.ContextLengthPool {
		if t.ContextWindow > r.lastTarget.ContextWindow && r.plan.Localities.Allows(t.Locality) && r.usable(t) {
			return t, true
		}
	}
	return model.Target{}, false
}

func (r *run) usable(t model.Target) bool {
	return !r.excluded[t.Name] && (!r.cloudBlocked || t.Locality != model.LocalityCloud)
}

// wait blocks for the backoff of a target already tried in this request and
// for its Retry-After. A target not tried yet is attempted immediately: the
// backoff protects an upstream from its own retries, not from fallback.
func (r *run) wait(ctx context.Context, t model.Target) error {
	var delay time.Duration
	if n := r.tries[t.Name]; n > 0 {
		delay = r.d.backoff(n)
	}
	if nb, ok := r.notBefore[t.Name]; ok {
		if w := nb.Sub(r.d.clk.Now()); w > delay {
			delay = w
		}
	}
	if delay <= 0 {
		return nil
	}
	select {
	case <-r.d.clk.After(delay):
		return nil
	case <-ctx.Done():
		return ctxErr(ctx)
	}
}

// prepare builds the provider request of an attempt on t: the original
// request (or its continuation with the produced text), masked when t is
// cloud and the mapping is not empty, with Model = t.Model.
func (r *run) prepare(
	ctx context.Context, t model.Target, continuation bool, produced string,
) (chat.Request, bool, error) {
	if _, ok := r.d.providers[t.Provider]; !ok {
		return chat.Request{}, false, fmt.Errorf("%w: %q", ErrUnknownProvider, t.Provider)
	}
	base := r.plan.Request
	cloud := t.Locality == model.LocalityCloud
	if continuation {
		base = continuationRequest(base, produced)
		if cloud {
			if err := r.rescan(ctx, produced); err != nil {
				return chat.Request{}, true, err
			}
		}
	}
	masked := cloud && r.mapping != nil && !r.mapping.Empty()
	var req chat.Request
	if masked {
		// MaskRequest replaces every mapped value everywhere, including the
		// partial assistant text of a continuation (values found by Rescan
		// are in the mapping now).
		req = r.mapping.MaskRequest(base)
	} else {
		req = base.Clone()
	}
	req.Model = t.Model
	if masked && r.mapping.Leaks(req) {
		return chat.Request{}, true, ErrMaskLeak
	}
	return req, masked, nil
}

// rescan adds the values detectors find in the produced text of a
// continuation to the mapping, creating the mapping when the
// request itself had no findings.
func (r *run) rescan(ctx context.Context, produced string) error {
	if len(r.plan.Detectors) == 0 {
		return nil
	}
	if r.mapping == nil {
		r.mapping = privacy.NewMapping(r.plan.Request, nil)
	}
	if _, err := r.mapping.Rescan(ctx, r.plan.Detectors, produced); err != nil {
		return fmt.Errorf("%w: %w", ErrRescan, err)
	}
	return nil
}

// continuationRequest is the original request plus the client-visible text
// produced so far as an assistant message and ContinuationInstruction.
func continuationRequest(orig chat.Request, produced string) chat.Request {
	c := orig.Clone()
	c.Messages = append(c.Messages,
		chat.TextMessage(chat.RoleAssistant, produced),
		chat.TextMessage(chat.RoleUser, ContinuationInstruction))
	return c
}

// attempt describes one attempt for the decision.
type attempt struct {
	target       model.Target
	masked       bool
	continuation bool
	start        time.Time
	usage        chat.Usage
	status       string
	err          error
}

// record appends the attempt to the result and adds its usage and cost.
func (r *run) record(a attempt) {
	cost := a.target.Pricing.Cost(a.usage)
	status := a.status
	if status == "" {
		status = statusOf(a.err)
	}
	r.res.Attempts = append(r.res.Attempts, decision.Attempt{
		Target:       a.target.Name,
		Model:        a.target.Model,
		Locality:     a.target.Locality,
		Masked:       a.masked,
		Continuation: a.continuation,
		Status:       status,
		HTTPStatus:   provider.StatusCode(a.err),
		Latency:      r.d.clk.Now().Sub(a.start),
		Usage:        a.usage,
		CostUSD:      cost,
	})
	r.res.Usage = r.res.Usage.Add(a.usage)
	r.res.CostUSD += cost
}

// refuse records a target refused before sending and excludes it (or every
// cloud target) from the rest of the request.
func (r *run) refuse(a attempt) {
	r.record(a)
	if errors.Is(a.err, ErrUnknownProvider) {
		r.excluded[a.target.Name] = true
	} else {
		r.cloudBlocked = true
	}
	r.lastErr, r.lastTarget = a.err, a.target
}

// begin counts an attempt that is about to be sent.
func (r *run) begin(t model.Target) {
	r.sent++
	r.tries[t.Name]++
}

// failed notes the failure of a sent attempt and its Retry-After.
func (r *run) failed(t model.Target, err error) {
	r.lastErr, r.lastTarget = err, t
	if ra := provider.RetryAfter(err); ra > 0 {
		if ra > r.d.opts.MaxRetryAfter {
			r.excluded[t.Name] = true
		} else {
			r.notBefore[t.Name] = r.d.clk.Now().Add(ra)
		}
	}
}

// report passes an attempt outcome to the Health port.
func (r *run) report(ctx context.Context, t model.Target, err error) {
	h := r.d.opts.Health
	if h == nil || (err != nil && ctx.Err() != nil) {
		return
	}
	if err != nil && !provider.IsRetryable(err) {
		err = nil
	}
	h.Report(t.Name, err)
}

// failure returns the dispatch error: why ctx ended, else the last attempt
// error, else ErrNoTarget.
func (r *run) failure(ctx context.Context, started bool) *Error {
	err := ctxErr(ctx)
	if err == nil {
		err = r.lastErr
	}
	if err == nil {
		err = ErrNoTarget
	}
	r.res.Target = r.lastTarget
	return &Error{Last: err, Attempts: append([]decision.Attempt(nil), r.res.Attempts...), Started: started}
}

// classify maps the error of an attempt whose context may have been cancelled
// by dispatch to the dispatch error: the caller's cancellation or the total
// timeout from ctx, the attempt timeout from actx.
func classify(ctx, actx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cerr := ctxErr(ctx); cerr != nil {
		return cerr
	}
	if errors.Is(context.Cause(actx), ErrAttemptTimeout) {
		return ErrAttemptTimeout
	}
	return err
}
