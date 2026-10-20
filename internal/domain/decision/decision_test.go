// SPDX-License-Identifier: Apache-2.0

package decision_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
)

func TestDecision_AddReason_Appends(t *testing.T) {
	t.Parallel()
	var d decision.Decision
	d.AddReason("a")
	d.AddReason("b")
	assert.Equal(t, []string{"a", "b"}, d.Reasons)
}

func TestDecision_FinalTarget_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    decision.Decision
		want string
	}{
		{name: "no attempts", d: decision.Decision{}, want: ""},
		{name: "one attempt", d: decision.Decision{Attempts: []decision.Attempt{{Target: "weak"}}}, want: "weak"},
		{
			name: "last attempt wins",
			d:    decision.Decision{Attempts: []decision.Attempt{{Target: "weak"}, {Target: "strong"}}},
			want: "strong",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.d.FinalTarget())
		})
	}
}

func TestReasonStrategy_Compare_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		strategy  string
		p, thresh float64
		want      string
	}{
		{name: "above", strategy: "bert", p: 0.71, thresh: 0.62, want: "strategy:bert p=0.71>=0.62"},
		{name: "below", strategy: "mf", p: 0.3, thresh: 0.62, want: "strategy:mf p=0.30<0.62"},
		{name: "equal", strategy: "bert", p: 0.62, thresh: 0.62, want: "strategy:bert p=0.62>=0.62"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, decision.ReasonStrategy(tc.strategy, tc.p, tc.thresh))
		})
	}
}

func TestReason_Formats_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "rule", got: decision.ReasonRule("length"), want: "rule:length"},
		{
			name: "strategy error",
			got:  decision.ReasonStrategyError("bert", model.TierStrong, "timeout"),
			want: "strategy:bert error->strong(timeout)",
		},
		{
			name: "strategy fallback",
			got:  decision.ReasonStrategyFallback("mf", "bert", "privacy"),
			want: "strategy:mf->bert(privacy)",
		},
		{name: "privacy", got: decision.ReasonPrivacy("local_only"), want: "privacy:local_only"},
		{name: "fallback", got: decision.ReasonFallback("429"), want: "fallback:429"},
		{name: "tier", got: decision.ReasonTier("escalate"), want: "tier:escalate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.got)
		})
	}
}
