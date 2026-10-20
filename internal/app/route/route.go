// SPDX-License-Identifier: Apache-2.0

package route

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// Client headers read by the pipeline (canonical names, as stored in
// chat.Request.Headers).
const (
	// HeaderPrivacy asks for a stricter privacy mode.
	HeaderPrivacy = "X-Triage-Privacy"
	// HeaderTier pins a tier when the route allows client override.
	HeaderTier = "X-Triage-Tier"
)

// Strategy names with a special meaning in the pipeline.
const (
	// StrategyNone disables the difficulty stage: rules only, else DefaultTier.
	StrategyNone = "none"
	// StrategyMF is the matrix-factorisation strategy whose external embedding
	// call triggers the privacy exceptions of the mask and local_only modes.
	StrategyMF = "mf"
)

// TierAction is what happens when the desired tier has no eligible target
// (routes[].on_tier_unavailable).
type TierAction string

// Tier actions. Locality constraints are never relaxed by any of them.
const (
	// TierEscalate moves a weak request to the strong tier (weak->strong only).
	TierEscalate TierAction = "escalate"
	// TierDegrade moves a strong request to the weak tier (strong->weak only).
	TierDegrade TierAction = "degrade"
	// TierFail fails the request with no_eligible_target.
	TierFail TierAction = "fail"
)

// Valid reports whether a is a known action.
func (a TierAction) Valid() bool { return a == TierEscalate || a == TierDegrade || a == TierFail }

// Rule is one entry of routes[].rules.match. Exactly one condition is set.
type Rule struct {
	// MinPromptTokens > 0 matches when the prompt token count (selection token
	// counter over chat.Request.PromptText) is at least this value.
	MinPromptTokens int
	// LastUserRegex, when non-nil, matches the text of the last user message.
	LastUserRegex *regexp.Regexp
	// Tier is the tier the rule selects.
	Tier model.Tier
}

// Rules is routes[].rules.
type Rules struct {
	// AllowClientOverride enables the X-Triage-Tier header rule, evaluated
	// before Match.
	AllowClientOverride bool
	// Match is evaluated in order; the first matching rule wins.
	Match []Rule
}

// Privacy is routes[].privacy.
type Privacy struct {
	// Mode is the route's privacy mode; it must be set explicitly.
	Mode privacy.Mode
	// MaxClientMode caps the mode a client may request with X-Triage-Privacy;
	// empty means local_only.
	MaxClientMode privacy.Mode
}

// Fallback is one entry of routes[].fallback: either a tier (written
// "tier:weak" / "tier:strong") or a target name.
type Fallback struct {
	// Tier is set for a tier entry.
	Tier model.Tier
	// Target is set for a target entry.
	Target string
}

// String returns the config spelling of the entry.
func (f Fallback) String() string {
	if f.Tier != model.TierNone {
		return "tier:" + string(f.Tier)
	}
	return f.Target
}

// ParseFallback parses a fallback entry: "tier:weak", "tier:strong" or a
// target name.
func ParseFallback(s string) (Fallback, error) {
	if rest, ok := strings.CutPrefix(s, "tier:"); ok {
		t := model.Tier(rest)
		if !t.Valid() {
			return Fallback{}, fmt.Errorf("route: fallback %q: unknown tier (want tier:weak or tier:strong)", s)
		}
		return Fallback{Tier: t}, nil
	}
	if s == "" {
		return Fallback{}, errors.New("route: empty fallback entry")
	}
	return Fallback{Target: s}, nil
}

// Route is one routes[] entry: a virtual model name and its policy. It is
// immutable once passed to New.
type Route struct {
	// Name is the virtual model clients send in the model field.
	Name string
	// Pool lists target names in priority order; empty means every target in
	// Deps.Targets order.
	Pool    []string
	Privacy Privacy
	Rules   Rules
	// Strategy is a key of Deps.Strategies, or "" / StrategyNone.
	Strategy string
	// Threshold is p_strong >= Threshold => strong.
	Threshold difficulty.Threshold
	// DefaultTier is used when no rule matches and Strategy is none; empty
	// means weak.
	DefaultTier model.Tier
	// OnTierUnavailable is the action when the desired tier has no eligible
	// target; empty means escalate.
	OnTierUnavailable TierAction
	// Fallback entries extend the chain after the desired tier's candidates.
	Fallback []Fallback
}
