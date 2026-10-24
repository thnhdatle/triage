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

type run struct {
	d       *Dispatcher
	plan    Plan
	chain   []model.Target
	mapping *privacy.Mapping
	res     Result

	sent            int
	cursor          int
	tries           map[string]int
	notBefore       map[string]time.Time
	excluded        map[string]bool
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

func ctxErr(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	return context.Cause(ctx)
}

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

func continuationRequest(orig chat.Request, produced string) chat.Request {
	c := orig.Clone()
	c.Messages = append(c.Messages,
		chat.TextMessage(chat.RoleAssistant, produced),
		chat.TextMessage(chat.RoleUser, ContinuationInstruction))
	return c
}

type attempt struct {
	target       model.Target
	masked       bool
	continuation bool
	start        time.Time
	usage        chat.Usage
	status       string
	err          error
}

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

func (r *run) refuse(a attempt) {
	r.record(a)
	if errors.Is(a.err, ErrUnknownProvider) {
		r.excluded[a.target.Name] = true
	} else {
		r.cloudBlocked = true
	}
	r.lastErr, r.lastTarget = a.err, a.target
}

func (r *run) begin(t model.Target) {
	r.sent++
	r.tries[t.Name]++
}

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
