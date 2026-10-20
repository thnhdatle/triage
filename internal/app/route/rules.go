// SPDX-License-Identifier: Apache-2.0

package route

import (
	"strings"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
)

// Rule reason names (decision reasons "rule:<name>").
const (
	ruleHeader = "header"
	ruleLength = "length"
	ruleRegex  = "regex"
)

// rules evaluates the route's rules: the X-Triage-Tier header when
// the route allows client override (an unknown value is ignored), then Match
// in order. It sets the tier and reports whether a rule matched. Rules only
// choose a tier; the locality constraint is applied by selection regardless.
func (r *request) rules() bool {
	rs := r.route.Rules
	if rs.AllowClientOverride {
		t := model.Tier(strings.ToLower(strings.TrimSpace(r.req.Headers[HeaderTier])))
		if t.Valid() {
			r.matched(t, ruleHeader)
			return true
		}
	}
	for _, m := range rs.Match {
		switch {
		case m.MinPromptTokens > 0:
			if r.promptTokens >= m.MinPromptTokens {
				r.matched(m.Tier, ruleLength)
				return true
			}
		case m.LastUserRegex != nil:
			if m.LastUserRegex.MatchString(r.req.LastUserText()) {
				r.matched(m.Tier, ruleRegex)
				return true
			}
		}
	}
	return false
}

func (r *request) matched(t model.Tier, rule string) {
	r.tier = t
	r.dec.AddReason(decision.ReasonRule(rule))
}
