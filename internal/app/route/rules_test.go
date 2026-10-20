// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/testutil/fakestrategy"
)

var proveRe = regexp.MustCompile(`(?i)\b(prove|derive)\b`)

func rulesRoute(override bool, match ...route.Rule) route.Route {
	r := baseRoute()
	r.Strategy = "bert"
	r.Threshold = 0.5
	r.Rules = route.Rules{AllowClientOverride: override, Match: match}
	return r
}

func TestPipeline_Chat_Rules(t *testing.T) {
	t.Parallel()
	lengthStrong := route.Rule{MinPromptTokens: 100, Tier: model.TierStrong}
	lengthWeak := route.Rule{MinPromptTokens: 100, Tier: model.TierWeak}
	regexStrong := route.Rule{LastUserRegex: proveRe, Tier: model.TierStrong}
	regexWeak := route.Rule{LastUserRegex: proveRe, Tier: model.TierWeak}
	tests := []struct {
		name       string
		route      route.Route
		header     string
		tokens     int
		text       string
		wantTier   model.Tier
		wantReason string
		wantScored bool
	}{
		{
			name: "header strong", route: rulesRoute(true, lengthWeak), header: "strong", tokens: 500,
			wantTier: model.TierStrong, wantReason: "rule:header",
		},
		{
			name: "header weak beats regex", route: rulesRoute(true, regexStrong), header: " WEAK ", text: "prove it",
			wantTier: model.TierWeak, wantReason: "rule:header",
		},
		{
			name: "header ignored without override", route: rulesRoute(false, regexWeak), header: "strong",
			text: "prove it", wantTier: model.TierWeak, wantReason: "rule:regex",
		},
		{
			name: "unknown header value ignored", route: rulesRoute(true, regexWeak), header: "medium",
			text: "prove it", wantTier: model.TierWeak, wantReason: "rule:regex",
		},
		{
			name: "length at threshold", route: rulesRoute(false, lengthStrong), tokens: 100,
			wantTier: model.TierStrong, wantReason: "rule:length",
		},
		{
			name: "length below threshold uses strategy", route: rulesRoute(false, lengthStrong), tokens: 99,
			wantTier: model.TierWeak, wantReason: "strategy:bert p=0.20<0.50", wantScored: true,
		},
		{
			name: "length before regex in order", route: rulesRoute(false, lengthWeak, regexStrong), tokens: 100,
			text: "prove it", wantTier: model.TierWeak, wantReason: "rule:length",
		},
		{
			name: "regex before length in order", route: rulesRoute(false, regexStrong, lengthWeak), tokens: 100,
			text: "Derive x", wantTier: model.TierStrong, wantReason: "rule:regex",
		},
		{
			name: "regex miss falls to next rule", route: rulesRoute(false, regexStrong, lengthWeak), tokens: 100,
			text: "approve", wantTier: model.TierWeak, wantReason: "rule:length",
		},
		{
			name: "no rule matches", route: rulesRoute(true, regexStrong, lengthStrong), tokens: 1, text: "hello",
			wantTier: model.TierWeak, wantReason: "strategy:bert p=0.20<0.50", wantScored: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bert := fakestrategy.Fixed("bert", 0.2)
			e := newEnv(t, setup{
				routes:     []route.Route{tc.route},
				strategies: map[string]difficulty.Strategy{"bert": bert},
				opts:       route.Options{Selection: route.SelectionOptions{Tokens: counter{n: tc.tokens}}},
			})
			req := userReq(tc.text)
			if tc.header != "" {
				req = withHeader(req, route.HeaderTier, tc.header)
			}
			_, err := e.p.Chat(context.Background(), req)
			require.NoError(t, err)
			d := e.only(t)
			assert.Equal(t, tc.wantTier, d.Tier)
			assert.Equal(t, []string{tc.wantReason}, d.Reasons)
			if tc.wantScored {
				assert.Equal(t, 1, bert.Calls())
				assert.Equal(t, "bert", d.Strategy)
				return
			}
			assert.Equal(t, 0, bert.Calls(), "a matching rule skips the strategy")
			assert.Empty(t, d.Strategy)
			assert.Nil(t, d.Score)
			assert.Nil(t, d.Threshold)
		})
	}
}

// TestPipeline_Chat_RuleKeepsPrivacy is the F4 DoD privacy invariant: a rule
// choosing a tier still selects within the allowed localities.
func TestPipeline_Chat_RuleKeepsPrivacy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rule route.Rule
		hdr  string
	}{
		{name: "header", hdr: "strong"},
		{name: "length", rule: route.Rule{MinPromptTokens: 1, Tier: model.TierStrong}},
		{name: "regex", rule: route.Rule{LastUserRegex: regexp.MustCompile(`mail`), Tier: model.TierStrong}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := rulesRoute(true)
			r.Strategy = route.StrategyNone
			if tc.rule.Tier != "" {
				r.Rules.Match = []route.Rule{tc.rule}
			}
			r.Privacy.Mode = privacy.ModeLocalOnly
			r.Fallback = []route.Fallback{{Tier: model.TierWeak}, {Target: "cloud-strong"}}
			e := newEnv(t, setup{routes: []route.Route{r}, detectors: []privacy.Detector{emailDetector()}})
			req := userReq("mail " + secret)
			if tc.hdr != "" {
				req = withHeader(req, route.HeaderTier, tc.hdr)
			}
			_, err := e.p.Chat(context.Background(), req)
			require.NoError(t, err)
			plan := e.disp.lastPlan(t)
			assert.Equal(t, []string{"local-strong", "local-weak"}, names(plan.Chain))
			assert.Equal(t, model.LocalOnly(), plan.Localities)
			assert.Equal(t, model.TierStrong, e.only(t).Tier)
		})
	}
}

// TestPipeline_Chat_ExactTokenCounter is the F3 DoD table: the length rule
// and the context-window filter use the configured counter's exact count.
func TestPipeline_Chat_ExactTokenCounter(t *testing.T) {
	t.Parallel()
	text := "short" // the estimate counts 2 tokens (+1 for the system message)
	tests := []struct {
		name       string
		tokens     chat.TokenCounter
		maxTokens  int
		wantReason string
		wantChain  []string
	}{
		{
			name: "estimate below rule", tokens: nil,
			wantReason: "default:weak", wantChain: []string{"local-weak", "cloud-weak"},
		},
		{
			name: "exact count meets rule", tokens: counter{n: 8000},
			wantReason: "rule:length", wantChain: []string{"local-weak", "cloud-weak"},
		},
		{
			name: "exact count drops small windows", tokens: counter{n: 9000}, maxTokens: 7000,
			wantReason: "rule:length", wantChain: []string{"cloud-weak"},
		},
		{
			name: "window boundary is inclusive", tokens: counter{n: 8000}, maxTokens: 192,
			wantReason: "rule:length", wantChain: []string{"local-weak", "cloud-weak"},
		},
		{
			name: "one past the window", tokens: counter{n: 8000}, maxTokens: 193,
			wantReason: "rule:length", wantChain: []string{"cloud-weak"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := baseRoute()
			r.Rules.Match = []route.Rule{{MinPromptTokens: 8000, Tier: model.TierWeak}}
			e := newEnv(t, setup{
				routes: []route.Route{r}, opts: route.Options{Selection: route.SelectionOptions{Tokens: tc.tokens}},
			})
			req := userReq(text)
			if tc.maxTokens > 0 {
				req.MaxCompletionTokens = &tc.maxTokens
			}
			_, err := e.p.Chat(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, []string{tc.wantReason}, e.only(t).Reasons)
			assert.Equal(t, tc.wantChain, names(e.disp.lastPlan(t).Chain))
		})
	}
}
