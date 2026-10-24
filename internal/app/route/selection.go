// SPDX-License-Identifier: Apache-2.0

package route

import (
	"fmt"
	"slices"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

type filter struct {
	localities model.LocalitySet
	need       int
	skipWindow bool
	caps       []model.Capability
	embeddings bool
}

func (p *Pipeline) eligible(t model.Target, f filter) bool {
	if t.IsEmbedding() != f.embeddings || !p.healthy(t) || !f.localities.Allows(t.Locality) {
		return false
	}
	if !f.skipWindow && t.ContextWindow < f.need {
		return false
	}
	for _, c := range f.caps {
		if !t.Has(c) {
			return false
		}
	}
	return true
}

func (r *request) chatFilter() filter {
	return filter{
		localities: r.localities(),
		need:       r.promptTokens + r.req.RequestedOutputTokens(),
		caps:       model.RequiredCapabilities(r.req),
	}
}

func (r *request) candidates(tier model.Tier, f filter, advance bool) []model.Target {
	ts := r.route.pool.Filter(func(t model.Target) bool {
		return t.Tier == tier && r.p.eligible(t, f)
	})
	return r.p.order(r.route, tier, ts, advance)
}

func (p *Pipeline) order(cr *compiledRoute, tier model.Tier, ts []model.Target, advance bool) []model.Target {
	if len(ts) < 2 {
		return ts
	}
	switch p.opts.Selection.Mode {
	case SelectRoundRobin:
		c := cr.rr[tier]
		n := c.Load()
		if advance {
			n = c.Add(1) - 1
		}
		k := int(n % uint64(len(ts))) //nolint:gosec // the remainder is below len(ts)
		return append(ts[k:len(ts):len(ts)], ts[:k]...)
	case SelectLeastInflight:
		slices.SortStableFunc(ts, func(a, b model.Target) int {
			x, y := p.inflight[a.Name].Load(), p.inflight[b.Name].Load()
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		})
	case SelectPriority:
	}
	return ts
}

func (r *request) selectChain() ([]model.Target, error) {
	f := r.chatFilter()
	primary := r.candidates(r.tier, f, true)
	if len(primary) == 0 {
		alt, action := r.unavailable()
		if action != "" {
			primary = r.candidates(alt, f, true)
		}
		if len(primary) == 0 {
			return nil, r.noTarget()
		}
		r.tier = alt
		r.dec.AddReason(decision.ReasonTier(string(action)))
	}
	chain := primary
	for _, fb := range r.route.Fallback {
		if fb.Tier != model.TierNone {
			chain = append(chain, r.candidates(fb.Tier, f, false)...)
			continue
		}
		if t, ok := r.p.deps.Targets.ByName(fb.Target); ok && !t.IsEmbedding() && r.p.eligible(t, f) {
			chain = append(chain, t)
		}
	}
	return dedupe(chain), nil
}

func (r *request) unavailable() (model.Tier, TierAction) {
	switch a := r.route.OnTierUnavailable; {
	case a == TierEscalate && r.tier == model.TierWeak:
		return model.TierStrong, a
	case a == TierDegrade && r.tier == model.TierStrong:
		return model.TierWeak, a
	}
	return model.TierNone, ""
}

func (r *request) noTarget() error {
	if r.localOnly {
		return fmt.Errorf("%w for route %q tier %s", privacy.ErrNoLocalTarget, r.route.Name, r.tier)
	}
	return fmt.Errorf("%w for route %q tier %s", ErrNoEligibleTarget, r.route.Name, r.tier)
}

func (r *request) contextLengthPool() []model.Target {
	f := r.chatFilter()
	f.skipWindow = true
	ts := r.route.pool.Filter(func(t model.Target) bool { return r.p.eligible(t, f) })
	slices.SortStableFunc(ts, func(a, b model.Target) int { return a.ContextWindow - b.ContextWindow })
	return ts
}

func dedupe(ts []model.Target) []model.Target {
	seen := make(map[string]bool, len(ts))
	out := ts[:0:0]
	for _, t := range ts {
		if !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	return out
}
