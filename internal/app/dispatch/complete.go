// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
)

// Complete runs the plan's chain until one target answers.
func (d *Dispatcher) Complete(ctx context.Context, plan Plan) (Result, error) {
	r := d.newRun(plan)
	ctx, stop := watch(ctx, d.clk, d.opts.TotalTimeout, ErrTotalTimeout)
	defer stop()
	for {
		t, ok := r.pick(ctx)
		if !ok {
			return r.res, r.failure(ctx, false)
		}
		resp, masked, ok := r.completeAttempt(ctx, t)
		if ok {
			r.succeed(t, resp, masked)
			return r.res, nil
		}
	}
}

func (r *run) completeAttempt(ctx context.Context, t model.Target) (chat.Response, bool, bool) {
	a := attempt{target: t, start: r.d.clk.Now()}
	req, masked, err := r.prepare(ctx, t, false, "")
	a.masked = masked
	if err != nil {
		a.err = err
		r.refuse(a)
		return chat.Response{}, false, false
	}
	r.begin(t)
	actx, stop := watch(ctx, r.d.clk, r.d.attemptTimeout(t), ErrAttemptTimeout)
	resp, err := r.d.providers[t.Provider].Complete(actx, req)
	err = classify(ctx, actx, err)
	stop()
	if err == nil {
		a.usage = resp.Usage
	}
	a.err = err
	r.record(a)
	r.report(ctx, t, err)
	if err != nil {
		r.failed(t, err)
		return chat.Response{}, masked, false
	}
	return resp, masked, true
}

func (r *run) succeed(t model.Target, resp chat.Response, masked bool) {
	if masked {
		var partial bool
		resp, partial = r.mapping.UnmaskResponse(resp)
		r.res.Unmask = decision.UnmaskOK
		if partial {
			r.res.Unmask = decision.UnmaskPartial
		}
	}
	resp.Usage = r.res.Usage
	r.res.Response = resp
	r.res.Target = t
	r.res.Model = resp.Model
}
