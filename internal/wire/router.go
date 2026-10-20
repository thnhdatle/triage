// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/thnhdatle/triage/internal/adapter/decisionlog"
	"github.com/thnhdatle/triage/internal/adapter/httpapi"
	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

// errShuttingDown is returned for a request that arrives after the last
// generation was retired (it maps to 503).
var errShuttingDown = fmt.Errorf("wire: shutting down: %w", route.ErrNoEligibleTarget)

// router serves requests on the current generation and annotates the
// `route request` span. It implements httpapi.Router, Catalog,
// ConfigView and the api settings source, all reading the live generation,
// so a reload is visible to the next request.
type router struct {
	live *live
}

var (
	_ httpapi.Router     = (*router)(nil)
	_ httpapi.Catalog    = (*router)(nil)
	_ httpapi.ConfigView = (*router)(nil)
)

// Chat implements httpapi.Router.
func (r *router) Chat(ctx context.Context, req chat.Request) (route.Outcome, error) {
	g := r.live.acquire()
	if g == nil {
		return route.Outcome{}, errShuttingDown
	}
	defer g.release()
	out, err := g.pipeline.Chat(ctx, req)
	annotate(ctx, "chat", out)
	return out, err //nolint:wrapcheck // the HTTP layer maps pipeline errors with route.Code
}

// ChatStream implements httpapi.Router.
func (r *router) ChatStream(ctx context.Context, req chat.Request, sink dispatch.Sink) (route.Outcome, error) {
	g := r.live.acquire()
	if g == nil {
		return route.Outcome{}, errShuttingDown
	}
	defer g.release()
	out, err := g.pipeline.ChatStream(ctx, req, sink)
	annotate(ctx, "chat", out)
	return out, err //nolint:wrapcheck // the HTTP layer maps pipeline errors with route.Code
}

// Embeddings implements httpapi.Router.
func (r *router) Embeddings(ctx context.Context, in route.EmbeddingsRequest) (route.Outcome, error) {
	g := r.live.acquire()
	if g == nil {
		return route.Outcome{}, errShuttingDown
	}
	defer g.release()
	out, err := g.pipeline.Embeddings(ctx, in)
	annotate(ctx, "embeddings", out)
	return out, err //nolint:wrapcheck // the HTTP layer maps pipeline errors with route.Code
}

// Routes implements httpapi.Catalog.
func (r *router) Routes() []string {
	if g := r.live.cur.Load(); g != nil {
		return append([]string(nil), g.routes...)
	}
	return nil
}

// Targets implements httpapi.Catalog.
func (r *router) Targets() []model.Target {
	if g := r.live.cur.Load(); g != nil {
		return append([]model.Target(nil), g.targets...)
	}
	return nil
}

// Redacted implements httpapi.ConfigView.
func (r *router) Redacted() any {
	if g := r.live.cur.Load(); g != nil {
		return g.cfg.Redacted()
	}
	return nil
}

// settings returns the reloadable api.* options.
func (r *router) settings() httpapi.Settings {
	g := r.live.cur.Load()
	if g == nil {
		return httpapi.Settings{}
	}
	a := g.cfg.API
	return httpapi.Settings{
		MaxBodyBytes: int64(a.MaxBodyBytes), ExposeTargets: a.ExposeTargets, ExposeDecisionDetail: a.ExposeDecisionDetail,
	}
}

// annotate sets the routing and GenAI attributes of the `route request`
// span: usage is the sum over attempts, so a single successful attempt
// carries the same usage as its parent.
func annotate(ctx context.Context, op string, out route.Outcome) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	d := out.Decision
	findings := 0
	for _, n := range d.Findings {
		findings += n
	}
	attrs := []attribute.KeyValue{
		attribute.String(attrOperation, op),
		attribute.String(attrRequestModel, out.Route),
		attribute.String("triage.route", out.Route),
		attribute.String("triage.decision_id", out.DecisionID),
		attribute.String("triage.privacy.mode", string(d.Privacy)),
		attribute.Int("triage.privacy.findings", findings),
		attribute.Float64("triage.cost_usd", d.CostUSD),
		attribute.Int(attrInputTokens, d.Usage.PromptTokens),
	}
	if op == "chat" {
		attrs = append(attrs, attribute.Int(attrOutputTokens, d.Usage.CompletionTokens))
	}
	if out.Model != "" {
		attrs = append(attrs, attribute.String(attrResponseModel, out.Model))
	}
	if d.Strategy != "" {
		attrs = append(attrs, attribute.String("triage.strategy", d.Strategy))
	}
	if d.Score != nil {
		attrs = append(attrs, attribute.Float64("triage.score", *d.Score))
	}
	if d.Tier != model.TierNone {
		attrs = append(attrs, attribute.String("triage.tier", string(d.Tier)))
	}
	if d.Error != "" {
		attrs = append(attrs, attribute.String("error.type", d.Error))
	}
	span.SetAttributes(attrs...)
}

// observedRecorder stores decisions and records the decision-derived
// metrics: cost by route and target (F9), fallback attempts (F10).
type observedRecorder struct {
	next decision.Recorder
	tel  *telemetry.Telemetry
	// costTarget names the target that prices mf scoring (F9).
	costTarget *atomic.Pointer[string]
}

// Record implements decision.Recorder.
func (o observedRecorder) Record(d decision.Decision) {
	o.next.Record(d)
	for i, a := range d.Attempts {
		o.tel.ObserveCost(d.Route, a.Target, a.CostUSD)
		if i > 0 {
			o.tel.ObserveFallback(d.Route, d.Attempts[i-1].Status)
		}
	}
	if t := o.costTarget.Load(); t != nil && *t != "" {
		o.tel.ObserveCost(d.Route, *t, d.ScoringCostUSD)
	}
}

// decisionStore adapts decisionlog.Log to httpapi.Decisions.
type decisionStore struct{ log *decisionlog.Log }

// List implements httpapi.Decisions.
func (s decisionStore) List(limit int, cursor string) ([]decision.Decision, string, error) {
	page, err := s.log.List(decisionlog.Filter{}, limit, cursor)
	if err != nil {
		return nil, "", fmt.Errorf("decisions: %w", err)
	}
	return page.Items, page.NextCursor, nil
}

// Get implements httpapi.Decisions.
func (s decisionStore) Get(id string) (decision.Decision, bool) { return s.log.Get(id) }
