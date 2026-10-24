// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/thnhdatle/triage/internal/adapter/piionnx"
	"github.com/thnhdatle/triage/internal/adapter/piiregex"
	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/health"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

type builder struct {
	env      Env
	log      *slog.Logger
	tel      *telemetry.Telemetry
	clk      clock.Clock
	health   *health.Tracker
	recorder decision.Recorder
	ort      *ortHolder

	mu    sync.Mutex
	cache map[string]*shared
}

type assembly struct {
	b      *builder
	ctx    context.Context //nolint:containedctx // lives only for one build call
	cfg    *config.Config
	p      *plan
	g      *generation
	layout *providerLayout
}

func (a *assembly) get(key string, mk func() (*shared, error)) (any, error) {
	if r, ok := a.g.resources[key]; ok {
		return r.value, nil
	}
	r, ok := a.b.cache[key]
	if !ok {
		var err error
		if r, err = mk(); err != nil {
			return nil, err
		}
	}
	r.refs.Add(1)
	a.g.resources[key] = r
	return r.value, nil
}

// build resolves and builds a generation from cfg.
func (b *builder) build(ctx context.Context, cfg *config.Config) (*generation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, err := resolve(ctx, cfg, b.env)
	if err != nil {
		return nil, err
	}
	for _, w := range p.warnings {
		b.log.Warn(w, "component", "config")
	}
	g := newGeneration()
	g.cfg, g.plan = cfg, p
	a := &assembly{b: b, ctx: ctx, cfg: cfg, p: p, g: g}
	if err := a.assemble(); err != nil {
		for _, e := range g.dispose(ctx) {
			b.log.Warn("close after failed build", "component", "config", "error", e)
		}
		return nil, err
	}
	return g, nil
}

func (a *assembly) assemble() error {
	provs, embedders, err := a.providers()
	if err != nil {
		return err
	}
	counter, err := a.counter()
	if err != nil {
		return err
	}
	if err := a.strategies(counter, provs); err != nil {
		return err
	}
	detectors, err := a.detectors()
	if err != nil {
		return err
	}
	a.g.detectors, a.g.counter = detectors, counter
	d := a.cfg.Dispatch
	disp, err := dispatch.New(provs, a.b.clk, dispatch.Options{
		MaxAttempts: d.MaxAttempts, AttemptTimeout: time.Duration(d.AttemptTimeout),
		TotalTimeout:  time.Duration(d.TotalTimeout),
		Backoff:       dispatch.Backoff{Base: time.Duration(d.Backoff.Base), Max: time.Duration(d.Backoff.Max)},
		MaxRetryAfter: time.Duration(d.MaxRetryAfter), StreamFailure: dispatch.StreamFailure(d.StreamFailure),
		ContextLengthFallback: d.ContextLengthFallback, Health: a.b.health,
	})
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	routes, err := routesOf(a.cfg, a.p)
	if err != nil {
		return err
	}
	pl, err := route.New(route.Deps{
		Targets: a.g.targets, Routes: routes, Strategies: a.g.strategies, Detectors: detectors,
		Dispatcher: disp, Embedders: embedders, Recorder: a.b.recorder, Health: a.b.health,
		Clock: a.b.clk, NewID: NewID, Observer: a.b.tel.Observer(),
	}, routeOptions(a.cfg, a.p, counter))
	if err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}
	a.g.pipeline = pl
	for _, r := range a.cfg.Routes {
		a.g.routes = append(a.g.routes, r.Name)
	}
	return nil
}

func (a *assembly) providers() (map[string]provider.Provider, map[string]route.Embedder, error) {
	layout, err := layoutProviders(a.cfg, a.b.env, a.cfg.Difficulty.MF.Provider)
	if err != nil {
		return nil, nil, err
	}
	a.layout = layout
	provs := map[string]provider.Provider{}
	embedders := map[string]route.Embedder{}
	byClient := map[string][]config.Target{}
	for _, t := range a.cfg.Targets {
		ck := layout.targetClient[t.Name]
		byClient[ck] = append(byClient[ck], t)
	}
	for _, ck := range slices.Sorted(maps.Keys(layout.specs)) {
		spec := layout.specs[ck]
		v, err := a.get(spec.cacheKey(), func() (*shared, error) {
			up, err := newUpstream(spec)
			if err != nil {
				return nil, err
			}
			return &shared{value: up, close: func(context.Context) error { up.CloseIdleConnections(); return nil }}, nil
		})
		if err != nil {
			return nil, nil, err
		}
		up := v.(upstream) //nolint:forcetypeassert // the key namespace "client:" only holds upstreams
		tp := &tracedProvider{next: up, name: spec.Provider, tel: a.b.tel, clk: a.b.clk, targets: map[string]string{}}
		for _, t := range byClient[ck] {
			if _, dup := tp.targets[t.Model]; !dup {
				tp.targets[t.Model] = t.Name
			}
		}
		provs[ck], embedders[ck] = tp, tp
		a.g.checks = append(a.g.checks, checksOf(a.cfg.Health.Probe, up, byClient[ck])...)
	}
	for _, t := range a.cfg.Targets {
		a.g.targets = append(a.g.targets, targetOf(t, layout.targetClient[t.Name]))
	}
	return provs, embedders, nil
}

func checksOf(kind string, up provider.Provider, ts []config.Target) []health.Check {
	if len(ts) == 0 {
		return nil
	}
	if kind != "completion" {
		names := make([]string, 0, len(ts))
		for _, t := range ts {
			names = append(names, t.Name)
		}
		return []health.Check{{Targets: names, Probe: probe(up, kind, "")}}
	}
	out := make([]health.Check, 0, len(ts))
	for _, t := range ts {
		if slices.Contains(t.Capabilities, string(model.CapEmbeddings)) {
			out = append(out, health.Check{Targets: []string{t.Name}, Probe: probe(up, "models", "")})
			continue
		}
		out = append(out, health.Check{Targets: []string{t.Name}, Probe: probe(up, kind, t.Model)})
	}
	return out
}

func targetOf(t config.Target, providerKey string) model.Target {
	caps := make([]model.Capability, 0, len(t.Capabilities))
	for _, c := range t.Capabilities {
		caps = append(caps, model.Capability(c))
	}
	return model.Target{
		Name: t.Name, Provider: providerKey, Model: t.Model, Locality: model.Locality(t.Locality),
		Tier: model.Tier(t.Tier), ContextWindow: t.ContextWindow, Capabilities: caps,
		Pricing: model.Pricing{
			InputPerMTok: t.Pricing.InputPerMTok, OutputPerMTok: t.Pricing.OutputPerMTok,
			CachedInputPerMTok: t.Pricing.CachedInputPerMTok,
		},
		AttemptTimeout: time.Duration(t.AttemptTimeout),
	}
}

func (a *assembly) counter() (chat.TokenCounter, error) {
	if a.p.tokenizer == "" {
		return nil, nil //nolint:nilnil // nil counter selects chat.Estimator
	}
	v, err := a.get("tokenizer:"+fileKey(a.p.tokenizer, ""), func() (*shared, error) {
		tok, err := hftok.Load(a.p.tokenizer)
		if err != nil {
			return nil, fmt.Errorf("selection.tokenizer %s: %w", a.p.tokenizer, err)
		}
		return &shared{value: tok}, nil
	})
	if err != nil {
		return nil, err
	}
	return tokenCounter{tok: v.(*hftok.Tokenizer)}, nil //nolint:forcetypeassert // "tokenizer:" keys hold tokenizers
}

func (a *assembly) strategies(counter chat.TokenCounter, provs map[string]provider.Provider) error {
	names := map[string]bool{}
	for _, s := range a.p.strategies {
		if s != strategyNone {
			names[s] = true
		}
	}
	if a.p.mfFallback != "" {
		names[a.p.mfFallback] = true
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		s, err := a.strategy(name, counter, provs)
		if err != nil {
			return err
		}
		a.g.strategies[name] = observedStrategy{next: s, tel: a.b.tel}
	}
	return nil
}

func (a *assembly) strategy(
	name string, counter chat.TokenCounter, provs map[string]provider.Provider,
) (difficulty.Strategy, error) {
	switch name {
	case strategyBERT:
		bc := bertConfig(*a.p.bert, a.cfg.Difficulty.BERT)
		v, err := a.get(bertKey(bc, a.p.bert.Key), func() (*shared, error) {
			rt, err := a.b.ort.get()
			if err != nil {
				return nil, fmt.Errorf("strategy bert: %w", err)
			}
			s, err := bert.Load(a.ctx, rt, bc)
			if err != nil {
				return nil, fmt.Errorf("strategy bert: %w", err)
			}
			return &shared{value: s, close: s.Close}, nil
		})
		if err != nil {
			return nil, err
		}
		return v.(difficulty.Strategy), nil //nolint:forcetypeassert // "bert:" keys hold strategies
	case strategyMF:
		m := a.cfg.Difficulty.MF
		ep, ok := provs[m.Provider].(*tracedProvider)
		if !ok {
			return nil, fmt.Errorf("strategy mf: unknown provider %q", m.Provider)
		}
		key := fmt.Sprintf("mf:%s|%d|%s", a.p.mfKey, m.CacheSize, a.layout.specs[m.Provider].cacheKey())
		v, err := a.get(key, func() (*shared, error) {
			s, err := newMF(ep, a.p.mfVector, m.CacheSize)
			if err != nil {
				return nil, err
			}
			return &shared{value: s}, nil
		})
		if err != nil {
			return nil, err
		}
		return v.(difficulty.Strategy), nil //nolint:forcetypeassert // "mf:" keys hold strategies
	}
	return simpleStrategy(name, a.cfg, counter)
}

func (a *assembly) detectors() ([]privacy.Detector, error) {
	var off []string
	for _, id := range piiregex.IDs() {
		if on, set := a.cfg.Privacy.Detectors[id]; set && !on && id != piiregex.TypeStreetAddress {
			off = append(off, id)
		}
	}
	re, err := piiregex.New(piiregex.WithStreetAddress(a.cfg.Privacy.Detectors[piiregex.TypeStreetAddress]),
		piiregex.WithDisabled(off...))
	if err != nil {
		return nil, fmt.Errorf("privacy detectors: %w", err)
	}
	dets := make([]privacy.Detector, 0, 2)
	dets = append(dets, re)
	if a.p.ner == nil {
		return dets, nil
	}
	n := a.cfg.Privacy.NER
	key := fmt.Sprintf("ner:%s|%v|%d|%d", a.p.ner.key, n.MinScore, n.MaxTokens, n.Timeout)
	v, err := a.get(key, func() (*shared, error) {
		rt, err := a.b.ort.get()
		if err != nil {
			return nil, fmt.Errorf("privacy.ner: %w", err)
		}
		det, err := piionnx.New(rt, a.p.ner.files, piionnx.WithMinScore(n.MinScore),
			piionnx.WithMaxTokens(n.MaxTokens), piionnx.WithTimeout(time.Duration(n.Timeout)),
			piionnx.WithLogger(a.b.log))
		if err != nil {
			return nil, fmt.Errorf("privacy.ner: %w", err)
		}
		if err := det.Probe(a.ctx); err != nil {
			return nil, errors.Join(fmt.Errorf("privacy.ner probe: %w", err), det.Close(a.ctx))
		}
		return &shared{value: det, close: det.Close}, nil
	})
	if err != nil {
		return nil, err
	}
	return append(dets, v.(privacy.Detector)), nil //nolint:forcetypeassert // "ner:" keys hold detectors
}

func routesOf(cfg *config.Config, p *plan) ([]route.Route, error) {
	out := make([]route.Route, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		rr := route.Route{
			Name: r.Name, Pool: slices.Clone(r.Pool),
			Privacy: route.Privacy{
				Mode: privacy.Mode(r.Privacy.Mode), MaxClientMode: privacy.Mode(r.Privacy.MaxClientMode),
			},
			Rules:     route.Rules{AllowClientOverride: r.Rules.AllowClientOverride},
			Strategy:  p.strategies[r.Name],
			Threshold: difficulty.Threshold(r.Threshold), DefaultTier: model.Tier(r.DefaultTier),
			OnTierUnavailable: route.TierAction(r.OnTierUnavailable),
		}
		for _, m := range r.Rules.Match {
			rule := route.Rule{MinPromptTokens: m.MinPromptTokens, Tier: model.Tier(m.Tier)}
			if m.LastUserRegex != "" {
				re, err := regexp.Compile(m.LastUserRegex)
				if err != nil {
					return nil, fmt.Errorf("route %s: %w", r.Name, err)
				}
				rule.LastUserRegex = re
			}
			rr.Rules.Match = append(rr.Rules.Match, rule)
		}
		for _, f := range r.Fallback {
			fb, err := route.ParseFallback(f)
			if err != nil {
				return nil, err
			}
			rr.Fallback = append(rr.Fallback, fb)
		}
		out = append(out, rr)
	}
	return out, nil
}

func routeOptions(cfg *config.Config, p *plan, counter chat.TokenCounter) route.Options {
	d := cfg.Difficulty
	return route.Options{
		Privacy: route.PrivacyOptions{
			ScanSystem: cfg.Privacy.ScanSystem, MaxScanBytes: int(cfg.Privacy.MaxScanBytes),
			OnOversize: privacy.OversizeAction(cfg.Privacy.OnOversize),
		},
		Difficulty: route.DifficultyOptions{
			OnError: model.Tier(d.OnError),
			Timeouts: map[string]time.Duration{
				strategyRandom: time.Duration(d.Timeout), strategyLength: time.Duration(d.Timeout),
				strategyBERT: time.Duration(d.BERT.Timeout), strategyMF: time.Duration(d.MF.Timeout),
			},
			MFFallback: p.mfFallback, MFCostTarget: p.mfCostTarget,
		},
		Selection: route.SelectionOptions{Mode: route.SelectionMode(cfg.Selection.Mode), Tokens: counter},
		Embeddings: route.EmbeddingOptions{
			MaxAttempts: cfg.Dispatch.MaxAttempts, AttemptTimeout: time.Duration(cfg.Dispatch.AttemptTimeout),
		},
	}
}

func (b *builder) commit(ctx context.Context, l *live, g *generation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h := g.cfg.Health
	b.health.SetOptions(health.Options{
		Interval: time.Duration(h.Interval), FailureThreshold: h.FailureThreshold, Cooldown: time.Duration(h.Cooldown),
	})
	b.cache = g.resources
	b.retire(ctx, l.swap(g))
}

func (b *builder) retire(ctx context.Context, old *generation) {
	if old == nil {
		return
	}
	fn := func() {
		for _, err := range old.dispose(ctx) {
			b.log.Warn("close retired resource", "component", "config", "error", err)
		}
	}
	old.onIdle.Store(&fn)
	old.release()
}

func (b *builder) shutdown(ctx context.Context, l *live) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cache = nil
	b.retire(ctx, l.swap(nil))
}
