// SPDX-License-Identifier: Apache-2.0

package route

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// Operations recorded in Decision.Operation.
const (
	OperationChat       = "chat"
	OperationEmbeddings = "embeddings"
)

// request is the state of one chat request through the stages. It is owned
// by the goroutine that called Chat or ChatStream.
type request struct {
	p     *Pipeline
	route *compiledRoute
	req   chat.Request
	dec   decision.Decision
	start time.Time

	// privacy stage
	mode      privacy.Mode
	scan      privacy.Result
	scanned   bool
	scanError bool
	localOnly bool
	mapping   *privacy.Mapping

	promptTokens int
	tier         model.Tier
}

// Chat routes a non-streaming chat request. The decision is recorded exactly
// once, also on error (with Decision.Error set to the Code of the error).
func (p *Pipeline) Chat(ctx context.Context, req chat.Request) (Outcome, error) {
	return p.chat(ctx, req, func(ctx context.Context, plan dispatch.Plan) (dispatch.Result, error) {
		return p.deps.Dispatcher.Complete(ctx, plan)
	})
}

// ChatStream routes a streaming chat request and relays it to sink. The
// decision is recorded exactly once, after the stream ended.
func (p *Pipeline) ChatStream(ctx context.Context, req chat.Request, sink dispatch.Sink) (Outcome, error) {
	return p.chat(ctx, req, func(ctx context.Context, plan dispatch.Plan) (dispatch.Result, error) {
		return p.deps.Dispatcher.Stream(ctx, plan, sink)
	})
}

type dispatchFunc func(ctx context.Context, plan dispatch.Plan) (dispatch.Result, error)

func (p *Pipeline) chat(ctx context.Context, req chat.Request, run dispatchFunc) (Outcome, error) {
	r := p.newRequest(ctx, req, OperationChat)
	res, err := r.run(ctx, run)
	return r.finish(res, err)
}

type decisionIDKey struct{}

// WithDecisionID makes the pipeline use id as the decision id of the request run
// with ctx, so a caller can announce the id before the outcome is known (streams).
func WithDecisionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, decisionIDKey{}, id)
}

func (p *Pipeline) newRequest(ctx context.Context, req chat.Request, op string) *request {
	start := p.deps.Clock.Now()
	id, _ := ctx.Value(decisionIDKey{}).(string)
	if id == "" {
		id = p.deps.NewID()
	}
	if req.ID == "" {
		req.ID = id
	}
	return &request{
		p:     p,
		req:   req,
		start: start,
		dec:   decision.Decision{ID: id, Route: req.Model, Time: start, Operation: op},
	}
}

// run runs every stage and the dispatch.
func (r *request) run(ctx context.Context, run dispatchFunc) (dispatch.Result, error) {
	if err := r.resolve(); err != nil {
		return dispatch.Result{}, err
	}
	if err := r.privacyStage(ctx); err != nil {
		return dispatch.Result{}, err
	}
	r.promptTokens = r.req.PromptTokens(r.p.opts.Selection.Tokens)
	if !r.rules() {
		r.difficultyStage(ctx)
	}
	plan, err := r.plan()
	if err != nil {
		return dispatch.Result{}, err
	}
	r.p.deps.Observer.Observe(Event{Kind: EventTier, Route: r.route.Name, Tier: r.tier})
	r.p.deps.Observer.Observe(Event{
		Kind: EventRouteOverhead, Route: r.route.Name, Duration: r.p.deps.Clock.Now().Sub(r.start),
	})
	primary := r.p.inflight[plan.Chain[0].Name]
	primary.Add(1)
	defer primary.Add(-1)
	return run(ctx, plan)
}

// resolve finds the route named by the model field.
func (r *request) resolve() error {
	cr, ok := r.p.routes[r.req.Model]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownRoute, r.req.Model)
	}
	r.route = cr
	return nil
}

// plan builds the dispatch plan from the selection.
func (r *request) plan() (dispatch.Plan, error) {
	chain, err := r.selectChain()
	if err != nil {
		return dispatch.Plan{}, err
	}
	plan := dispatch.Plan{
		Request:           r.req,
		Chain:             chain,
		Localities:        r.localities(),
		Mapping:           r.mapping,
		ContextLengthPool: r.contextLengthPool(),
	}
	if r.mode == privacy.ModeMask {
		plan.Detectors = r.p.deps.Detectors
	}
	return plan, nil
}

// finish completes and records the decision and builds the outcome.
func (r *request) finish(res dispatch.Result, err error) (Outcome, error) {
	d := &r.dec
	d.Attempts = res.Attempts
	d.Usage = res.Usage
	d.CostUSD = res.CostUSD + d.ScoringCostUSD
	d.Unmask = res.Unmask
	d.Tier = r.tier
	if res.Target.Name != "" && res.Target.Tier.Valid() {
		d.Tier = res.Target.Tier
	}
	for i := 0; i+1 < len(res.Attempts); i++ {
		d.AddReason(decision.ReasonFallback(attemptCause(res.Attempts[i])))
	}
	if res.Unmask == decision.UnmaskPartial {
		r.p.deps.Observer.Observe(Event{Kind: EventUnmaskFailure, Route: d.Route})
	}
	if err != nil {
		d.Error, _ = Code(err)
	}
	r.p.deps.Recorder.Record(*d)
	return Outcome{
		Decision:   *d,
		DecisionID: d.ID,
		Route:      d.Route,
		Target:     res.Target.Name,
		Model:      res.Model,
		Response:   res.Response,
	}, err
}

// attemptCause is the fallback reason cause of a failed attempt: its HTTP
// status when one was received, else its status class.
func attemptCause(a decision.Attempt) string {
	if a.HTTPStatus > 0 {
		return strconv.Itoa(a.HTTPStatus)
	}
	return a.Status
}
