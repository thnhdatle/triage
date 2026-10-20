// SPDX-License-Identifier: Apache-2.0

package route

import (
	"context"
	"errors"
	"fmt"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// errEmbedTimeout ends an embeddings attempt that exceeded its timeout; it
// matches provider.ErrTimeout and is retryable.
var errEmbedTimeout = fmt.Errorf("route: embeddings attempt timeout: %w", provider.ErrTimeout)

// EmbeddingsRequest is a /v1/embeddings request: Request.Model is the route
// name.
type EmbeddingsRequest struct {
	// ID is the request id; empty means the decision id.
	ID string
	// Headers holds the X-Triage-* client headers by canonical name.
	Headers map[string]string
	Request provider.EmbedRequest
}

// Embeddings routes an embeddings request: the route's pool
// restricted to embedding targets, the privacy stage with mask findings
// handled as local_only (vectors cannot be unmasked), no rules and no
// difficulty stage. Eligible targets are tried in selection order, each at
// most once, up to Options.Embeddings.MaxAttempts, moving on only after a
// retryable error (provider.IsRetryable). The decision is recorded exactly
// once.
func (p *Pipeline) Embeddings(ctx context.Context, in EmbeddingsRequest) (Outcome, error) {
	msgs := make([]chat.Message, len(in.Request.Input))
	for i, s := range in.Request.Input {
		msgs[i] = chat.TextMessage(chat.RoleUser, s)
	}
	r := p.newRequest(ctx, chat.Request{ID: in.ID, Model: in.Request.Model, Messages: msgs, Headers: in.Headers},
		OperationEmbeddings)
	resp, res, err := r.embed(ctx, in.Request)
	out, err := r.finish(res, err)
	out.Model = resp.Model
	out.Embeddings = resp
	return out, err
}

func (r *request) embed(
	ctx context.Context, in provider.EmbedRequest,
) (provider.EmbedResponse, dispatch.Result, error) {
	if err := r.resolve(); err != nil {
		return provider.EmbedResponse{}, dispatch.Result{}, err
	}
	mode, err := effectiveMode(r.route.Privacy, r.req.Headers)
	if err != nil {
		return provider.EmbedResponse{}, dispatch.Result{}, err
	}
	r.mode = mode
	r.dec.Privacy = mode
	if mode != privacy.ModeOff {
		if err := r.runScan(ctx, r.req, false); err != nil {
			return provider.EmbedResponse{}, dispatch.Result{}, err
		}
		if r.scanError || r.scan.HasFindings() {
			r.restrictLocal()
		}
	}
	f := filter{localities: r.localities(), skipWindow: true, embeddings: true}
	ts := r.route.pool.Filter(func(t model.Target) bool { return r.p.eligible(t, f) })
	chain := r.p.order(r.route, model.TierNone, ts, true)
	if len(chain) == 0 {
		return provider.EmbedResponse{}, dispatch.Result{}, r.noTarget()
	}
	if n := r.p.opts.Embeddings.MaxAttempts; len(chain) > n {
		chain = chain[:n]
	}
	r.p.deps.Observer.Observe(Event{
		Kind: EventRouteOverhead, Route: r.route.Name, Duration: r.p.deps.Clock.Now().Sub(r.start),
	})
	return r.embedChain(ctx, in, chain)
}

// embedChain tries the chain in order until an attempt succeeds or fails
// with a non-retryable error.
func (r *request) embedChain(
	ctx context.Context, in provider.EmbedRequest, chain []model.Target,
) (provider.EmbedResponse, dispatch.Result, error) {
	var res dispatch.Result
	var last error
	for _, t := range chain {
		if err := ctx.Err(); err != nil {
			last = err
			break
		}
		resp, a, err := r.embedAttempt(ctx, in, t)
		res.Attempts = append(res.Attempts, a)
		res.Usage = res.Usage.Add(a.Usage)
		res.CostUSD += a.CostUSD
		res.Target = t
		if err == nil {
			res.Model = resp.Model
			return resp, res, nil
		}
		last = err
		if !errors.Is(err, dispatch.ErrUnknownProvider) && !provider.IsRetryable(err) {
			break
		}
	}
	return provider.EmbedResponse{}, res, fmt.Errorf("route: embeddings: %d attempt(s): %w", len(res.Attempts), last)
}

func (r *request) embedAttempt(
	ctx context.Context, in provider.EmbedRequest, t model.Target,
) (provider.EmbedResponse, decision.Attempt, error) {
	clk := r.p.deps.Clock
	a := decision.Attempt{Target: t.Name, Model: t.Model, Locality: t.Locality}
	start := clk.Now()
	e, ok := r.p.deps.Embedders[t.Provider]
	if !ok || e == nil {
		err := fmt.Errorf("%w: %q", dispatch.ErrUnknownProvider, t.Provider)
		a.Status = attemptStatus(err)
		return provider.EmbedResponse{}, a, err
	}
	req := in
	req.Input = append([]string(nil), in.Input...)
	req.Model = t.Model
	timeout := r.p.opts.Embeddings.AttemptTimeout
	if t.AttemptTimeout > 0 {
		timeout = t.AttemptTimeout
	}
	actx, stop := withClockTimeout(ctx, clk, timeout, errEmbedTimeout)
	resp, err := e.Embed(actx, req)
	if err != nil && ctx.Err() == nil && errors.Is(context.Cause(actx), errEmbedTimeout) {
		err = errEmbedTimeout
	}
	stop()
	a.Latency = clk.Now().Sub(start)
	a.Status = attemptStatus(err)
	a.HTTPStatus = provider.StatusCode(err)
	if err != nil {
		return provider.EmbedResponse{}, a, err
	}
	a.Usage = resp.Usage
	a.CostUSD = t.Pricing.Cost(resp.Usage)
	return resp, a, nil
}

// attemptStatus is the decision status class of an embeddings attempt, in
// the vocabulary of dispatch.
func attemptStatus(err error) string {
	checks := []struct {
		target error
		status string
	}{
		{context.Canceled, "canceled"},
		{dispatch.ErrUnknownProvider, "unknown_provider"},
		{provider.ErrRateLimited, "rate_limited"},
		{provider.ErrTimeout, "timeout"},
		{context.DeadlineExceeded, "timeout"},
		{provider.ErrContextLength, "context_length"},
		{provider.ErrBadRequest, "bad_request"},
		{provider.ErrUpstreamUnavailable, "unavailable"},
	}
	if err == nil {
		return "ok"
	}
	for _, c := range checks {
		if errors.Is(err, c.target) {
			return c.status
		}
	}
	if provider.StatusCode(err) >= 500 {
		return "server_error"
	}
	return "error"
}
