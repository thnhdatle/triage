// SPDX-License-Identifier: Apache-2.0

package route

import (
	"fmt"
	"slices"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// filter is the eligibility test of selection.
type filter struct {
	localities model.LocalitySet
	// need is prompt tokens + requested output tokens; checked against the
	// context window unless skipWindow.
	need       int
	skipWindow bool
	caps       []model.Capability
	// embeddings selects embedding targets instead of chat targets.
	embeddings bool
}

// eligible reports whether t passes every filter.
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

// candidates returns the eligible pool targets of tier in selection order.
// advance moves the round-robin rotation; it is set for the primary tier
// only, so one request advances it once.
func (r *request) candidates(tier model.Tier, f filter, advance bool) []model.Target {
	ts := r.route.pool.Filter(func(t model.Target) bool {
		return t.Tier == tier && r.p.eligible(t, f)
	})
	return r.p.order(r.route, tier, ts, advance)
}

// order sorts eligible targets by the selection mode.
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

// selectChain picks the tier's candidates, applies on_tier_unavailable, and
// appends the fallback entries. Every entry passes the same
// filter, so the locality constraint holds over the whole chain.
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

// unavailable returns the alternative tier of on_tier_unavailable, or "" as
// action when the route fails instead.
func (r *request) unavailable() (model.Tier, TierAction) {
	switch a := r.route.OnTierUnavailable; {
	case a == TierEscalate && r.tier == model.TierWeak:
		return model.TierStrong, a
	case a == TierDegrade && r.tier == model.TierStrong:
		return model.TierWeak, a
	}
	return model.TierNone, ""
}

// noTarget is the selection failure: privacy.ErrNoLocalTarget when the
// request is restricted to local targets (never relaxed to cloud), else
// ErrNoEligibleTarget.
func (r *request) noTarget() error {
	if r.localOnly {
		return fmt.Errorf("%w for route %q tier %s", privacy.ErrNoLocalTarget, r.route.Name, r.tier)
	}
	return fmt.Errorf("%w for route %q tier %s", ErrNoEligibleTarget, r.route.Name, r.tier)
}

// contextLengthPool returns the targets eligible for the context-length
// fallback: every eligible chat target of the route's pool, any tier,
// ignoring the context-window filter, by ascending window (ties in pool
// order), so dispatch takes the smallest larger window.
func (r *request) contextLengthPool() []model.Target {
	f := r.chatFilter()
	f.skipWindow = true
	ts := r.route.pool.Filter(func(t model.Target) bool { return r.p.eligible(t, f) })
	slices.SortStableFunc(ts, func(a, b model.Target) int { return a.ContextWindow - b.ContextWindow })
	return ts
}

// dedupe drops repeated target names, keeping the first occurrence.
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
