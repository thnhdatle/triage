// SPDX-License-Identifier: Apache-2.0

package route

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

type Dispatcher interface {
	Complete(ctx context.Context, plan dispatch.Plan) (dispatch.Result, error)
	Stream(ctx context.Context, plan dispatch.Plan, sink dispatch.Sink) (dispatch.Result, error)
}

type Embedder interface {
	Embed(ctx context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error)
}

type Health interface {
	IsHealthy(target string) bool
}

type Deps struct {
	Targets    model.Pool
	Routes     []Route
	Strategies map[string]difficulty.Strategy
	Detectors  []privacy.Detector
	Dispatcher Dispatcher
	Embedders  map[string]Embedder
	Recorder   decision.Recorder
	Health     Health
	Clock      clock.Clock
	NewID      func() string
	Observer   Observer
}

// Pipeline runs the routing stages for a request.
type Pipeline struct {
	deps         Deps
	opts         Options
	routes       map[string]*compiledRoute
	inflight     map[string]*atomic.Int64
	scoringPrice float64
}

type compiledRoute struct {
	Route
	pool       model.Pool
	strategy   difficulty.Strategy
	mfFallback difficulty.Strategy
	rr         map[model.Tier]*atomic.Uint64
}

// New builds a Pipeline from its dependencies and options.
func New(deps Deps, opts Options) (*Pipeline, error) {
	if err := checkDeps(deps); err != nil {
		return nil, err
	}
	opts, err := withDefaults(opts)
	if err != nil {
		return nil, err
	}
	if deps.Observer == nil {
		deps.Observer = NopObserver{}
	}
	p := &Pipeline{
		deps:     deps,
		opts:     opts,
		routes:   make(map[string]*compiledRoute, len(deps.Routes)),
		inflight: make(map[string]*atomic.Int64, len(deps.Targets)),
	}
	for _, t := range deps.Targets {
		if t.Name == "" {
			return nil, errors.New("route: target with empty name")
		}
		if _, dup := p.inflight[t.Name]; dup {
			return nil, fmt.Errorf("route: duplicate target %q", t.Name)
		}
		p.inflight[t.Name] = &atomic.Int64{}
	}
	if t, ok := deps.Targets.ByName(opts.Difficulty.MFCostTarget); ok {
		p.scoringPrice = t.Pricing.InputPerMTok
	}
	for _, r := range deps.Routes {
		cr, err := p.compile(r)
		if err != nil {
			return nil, err
		}
		if _, dup := p.routes[r.Name]; dup {
			return nil, fmt.Errorf("route: duplicate route %q", r.Name)
		}
		p.routes[r.Name] = cr
	}
	return p, nil
}

func checkDeps(d Deps) error {
	switch {
	case d.Dispatcher == nil:
		return errors.New("route: nil dispatcher")
	case d.Recorder == nil:
		return errors.New("route: nil recorder")
	case d.Clock == nil:
		return errors.New("route: nil clock")
	case d.NewID == nil:
		return errors.New("route: nil id generator")
	}
	return nil
}

func withDefaults(o Options) (Options, error) {
	if o.Difficulty.OnError == model.TierNone {
		o.Difficulty.OnError = model.TierStrong
	}
	if !o.Difficulty.OnError.Valid() {
		return o, fmt.Errorf("route: unknown difficulty.on_error %q", o.Difficulty.OnError)
	}
	if o.Selection.Mode == "" {
		o.Selection.Mode = SelectPriority
	}
	if !o.Selection.Mode.Valid() {
		return o, fmt.Errorf("route: unknown selection.mode %q", o.Selection.Mode)
	}
	if o.Selection.Tokens == nil {
		o.Selection.Tokens = chat.Estimator{}
	}
	if o.Privacy.OnOversize == "" {
		o.Privacy.OnOversize = privacy.OversizeLocalOnly
	}
	if _, err := privacy.ParseOversizeAction(string(o.Privacy.OnOversize)); err != nil {
		return o, fmt.Errorf("route: %w", err)
	}
	if o.Embeddings.MaxAttempts <= 0 {
		o.Embeddings.MaxAttempts = DefaultEmbedMaxAttempts
	}
	if o.Embeddings.AttemptTimeout <= 0 {
		o.Embeddings.AttemptTimeout = DefaultEmbedAttemptTimeout
	}
	return o, nil
}

func (p *Pipeline) compile(r Route) (*compiledRoute, error) {
	if r.Name == "" {
		return nil, errors.New("route: route with empty name")
	}
	if err := normalise(&r); err != nil {
		return nil, fmt.Errorf("route %q: %w", r.Name, err)
	}
	cr := &compiledRoute{Route: r, rr: map[model.Tier]*atomic.Uint64{
		model.TierNone: {}, model.TierWeak: {}, model.TierStrong: {},
	}}
	if len(r.Pool) == 0 {
		cr.pool = append(model.Pool(nil), p.deps.Targets...)
	}
	for _, name := range r.Pool {
		t, ok := p.deps.Targets.ByName(name)
		if !ok {
			return nil, fmt.Errorf("route %q: unknown pool target %q", r.Name, name)
		}
		cr.pool = append(cr.pool, t)
	}
	for _, f := range r.Fallback {
		if _, ok := p.deps.Targets.ByName(f.Target); f.Tier == model.TierNone && !ok {
			return nil, fmt.Errorf("route %q: unknown fallback target %q", r.Name, f.Target)
		}
	}
	if err := p.resolveStrategies(cr); err != nil {
		return nil, fmt.Errorf("route %q: %w", r.Name, err)
	}
	return cr, nil
}

func normalise(r *Route) error {
	if !r.Privacy.Mode.Valid() {
		return fmt.Errorf("unknown privacy mode %q", r.Privacy.Mode)
	}
	if r.Privacy.MaxClientMode == "" {
		r.Privacy.MaxClientMode = privacy.ModeLocalOnly
	}
	if !r.Privacy.MaxClientMode.Valid() {
		return fmt.Errorf("unknown max_client_mode %q", r.Privacy.MaxClientMode)
	}
	if r.DefaultTier == model.TierNone {
		r.DefaultTier = model.TierWeak
	}
	if !r.DefaultTier.Valid() {
		return fmt.Errorf("unknown default_tier %q", r.DefaultTier)
	}
	if r.OnTierUnavailable == "" {
		r.OnTierUnavailable = TierEscalate
	}
	if !r.OnTierUnavailable.Valid() {
		return fmt.Errorf("unknown on_tier_unavailable %q", r.OnTierUnavailable)
	}
	if !(r.Threshold >= 0 && r.Threshold <= 1) {
		return fmt.Errorf("threshold %v outside [0,1]", float64(r.Threshold))
	}
	if r.Strategy == "" {
		r.Strategy = StrategyNone
	}
	return checkRules(r)
}

func checkRules(r *Route) error {
	for i, m := range r.Rules.Match {
		hasLen, hasRe := m.MinPromptTokens > 0, m.LastUserRegex != nil
		if hasLen == hasRe || m.MinPromptTokens < 0 {
			return fmt.Errorf("rule %d: want exactly one of min_prompt_tokens > 0 or last_user_regex", i)
		}
		if !m.Tier.Valid() {
			return fmt.Errorf("rule %d: unknown tier %q", i, m.Tier)
		}
	}
	for i, f := range r.Fallback {
		if (f.Tier == model.TierNone) == (f.Target == "") || (f.Tier != model.TierNone && !f.Tier.Valid()) {
			return fmt.Errorf("fallback %d: want exactly one of a valid tier or a target", i)
		}
	}
	return nil
}

func (p *Pipeline) resolveStrategies(cr *compiledRoute) error {
	if cr.Strategy == StrategyNone {
		return nil
	}
	s, ok := p.deps.Strategies[cr.Strategy]
	if !ok || s == nil {
		return fmt.Errorf("unknown strategy %q", cr.Strategy)
	}
	cr.strategy = s
	if cr.Strategy != StrategyMF {
		return nil
	}
	name := p.opts.Difficulty.MFFallback
	fb, ok := p.deps.Strategies[name]
	if !ok || fb == nil || name == StrategyMF || name == StrategyNone {
		return fmt.Errorf("mf needs difficulty.mf.fallback naming another strategy, got %q", name)
	}
	cr.mfFallback = fb
	return nil
}

type Outcome struct {
	Decision   decision.Decision
	DecisionID string
	Route      string
	Target     string
	Model      string
	Response   chat.Response
	Embeddings provider.EmbedResponse
}

func (p *Pipeline) healthy(t model.Target) bool {
	return p.deps.Health == nil || p.deps.Health.IsHealthy(t.Name)
}
