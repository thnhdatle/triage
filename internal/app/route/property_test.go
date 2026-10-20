// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil/fakedetector"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

func drawTargets(rt *rapid.T) (model.Pool, healthSet) {
	n := rapid.IntRange(1, 7).Draw(rt, "targets")
	pool := make(model.Pool, n)
	health := healthSet{}
	for i := range n {
		var caps []model.Capability
		for _, c := range []model.Capability{model.CapTools, model.CapVision, model.CapJSONMode} {
			if rapid.Bool().Draw(rt, fmt.Sprintf("t%d-%s", i, c)) {
				caps = append(caps, c)
			}
		}
		if rapid.IntRange(0, 5).Draw(rt, fmt.Sprintf("t%d-embed", i)) == 0 {
			caps = append(caps, model.CapEmbeddings)
		}
		pool[i] = target(fmt.Sprintf("t%d", i),
			rapid.SampledFrom([]model.Locality{model.LocalityLocal, model.LocalityCloud}).Draw(rt, fmt.Sprintf("t%d-loc", i)),
			rapid.SampledFrom([]model.Tier{model.TierWeak, model.TierStrong}).Draw(rt, fmt.Sprintf("t%d-tier", i)),
			rapid.SampledFrom([]int{0, 50, 4000, 20000}).Draw(rt, fmt.Sprintf("t%d-window", i)),
			caps...)
		if rapid.IntRange(0, 3).Draw(rt, fmt.Sprintf("t%d-down", i)) == 0 {
			health[pool[i].Name] = true
		}
	}
	return pool, health
}

func drawRequest(rt *rapid.T) chat.Request {
	text := rapid.SampledFrom([]string{"hello", "mail " + secret, strings.Repeat("long text ", 40)}).Draw(rt, "text")
	req := userReq(text)
	if rapid.Bool().Draw(rt, "tools") {
		req.Tools = []chat.Tool{{Type: "function"}}
	}
	if rapid.Bool().Draw(rt, "image") {
		req.Messages[1].Content = append(req.Messages[1].Content, chat.Part{Type: chat.PartImage, ImageURL: "u"})
	}
	if rapid.Bool().Draw(rt, "json") {
		req.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
	}
	if out := rapid.IntRange(0, 6000).Draw(rt, "max_tokens"); out > 0 {
		req.MaxTokens = &out
	}
	return req
}

func drawRoute(rt *rapid.T, pool model.Pool) route.Route {
	r := baseRoute()
	modes := []privacy.Mode{privacy.ModeOff, privacy.ModeMask, privacy.ModeLocalOnly}
	r.Privacy.Mode = rapid.SampledFrom(modes).Draw(rt, "mode")
	r.DefaultTier = rapid.SampledFrom([]model.Tier{model.TierWeak, model.TierStrong}).Draw(rt, "tier")
	r.OnTierUnavailable = rapid.SampledFrom([]route.TierAction{
		route.TierEscalate, route.TierDegrade, route.TierFail,
	}).Draw(rt, "on_tier_unavailable")
	entries := make([]string, 0, 2+len(pool))
	entries = append(entries, "tier:weak", "tier:strong")
	for _, t := range pool {
		entries = append(entries, t.Name)
	}
	for _, s := range rapid.SliceOfN(rapid.SampledFrom(entries), 0, 4).Draw(rt, "fallback") {
		f, err := route.ParseFallback(s)
		require.NoError(rt, err)
		r.Fallback = append(r.Fallback, f)
	}
	return r
}

// TestPipeline_Chat_SelectionProperty is the F3 DoD property over the whole
// chain: no unhealthy target, none with a too small context window, none
// lacking a required capability, none outside the allowed localities, and
// (F5) only local targets for a local_only request with a finding.
func TestPipeline_Chat_SelectionProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		pool, health := drawTargets(rt)
		r := drawRoute(rt, pool)
		req := drawRequest(rt)
		mode := rapid.SampledFrom([]route.SelectionMode{
			route.SelectPriority, route.SelectRoundRobin, route.SelectLeastInflight,
		}).Draw(rt, "selection")
		e := newEnv(rt, setup{
			targets: pool, routes: []route.Route{r}, health: health,
			detectors: []privacy.Detector{emailDetector()},
			opts:      route.Options{Selection: route.SelectionOptions{Mode: mode}},
		})
		_, err := e.p.Chat(context.Background(), req)
		finding := strings.Contains(req.LastUserText(), secret)
		localOnly := r.Privacy.Mode == privacy.ModeLocalOnly && finding
		if err != nil {
			if localOnly {
				require.ErrorIs(rt, err, privacy.ErrNoLocalTarget)
			} else {
				require.ErrorIs(rt, err, route.ErrNoEligibleTarget)
			}
			require.Equal(rt, 0, e.disp.calls())
			return
		}
		plan := e.disp.lastPlan(rt)
		require.NotEmpty(rt, plan.Chain)
		checkPlan(rt, plan, req, health, localOnly)
		if localOnly {
			assert.Equal(rt, model.LocalOnly(), plan.Localities)
		}
	})
}

// checkPlan asserts the selection invariants over the chain and the
// context-length pool.
func checkPlan(rt *rapid.T, plan dispatch.Plan, req chat.Request, health healthSet, localOnly bool) {
	need := chat.EstimateTokens(req.PromptText()) + req.RequestedOutputTokens()
	caps := model.RequiredCapabilities(req)
	check := func(tg model.Target, window bool) {
		assert.False(rt, health[tg.Name], "unhealthy %s", tg.Name)
		assert.False(rt, tg.IsEmbedding(), "embedding target %s", tg.Name)
		assert.True(rt, plan.Localities.Allows(tg.Locality), "locality of %s", tg.Name)
		if window {
			assert.GreaterOrEqual(rt, tg.ContextWindow, need, "window of %s", tg.Name)
		}
		for _, c := range caps {
			assert.True(rt, tg.Has(c), "%s lacks %s", tg.Name, c)
		}
		if localOnly {
			assert.Equal(rt, model.LocalityLocal, tg.Locality, "%s is not local", tg.Name)
		}
	}
	seen := map[string]bool{}
	for _, tg := range plan.Chain {
		check(tg, true)
		assert.False(rt, seen[tg.Name], "duplicate %s", tg.Name)
		seen[tg.Name] = true
	}
	for _, tg := range plan.ContextLengthPool {
		check(tg, false)
	}
}

var emailRe = regexp.MustCompile(`[a-z]{3,8}@example\.com`)

// realPipeline wires the real dispatcher with one fake provider per target.
func realPipeline(
	t testingT, r route.Route, outcomes map[string][]fakeprovider.Outcome,
) (env, map[string]*fakeprovider.Provider) {
	t.Helper()
	pool := model.Pool{localWeak, cloudWeak, localStrong, cloudStrong}
	fps := map[string]*fakeprovider.Provider{}
	providers := map[string]provider.Provider{}
	for _, tg := range pool {
		fp := fakeprovider.New(fakeprovider.WithOutcomes(outcomes[tg.Name]...))
		fps[tg.Name], providers[tg.Provider] = fp, fp
	}
	d, err := dispatch.New(providers, clock.Real(), dispatch.Options{
		Backoff: dispatch.Backoff{Base: time.Microsecond, Max: time.Millisecond}, MaxAttempts: 4,
	})
	require.NoError(t, err)
	det := fakedetector.NewRegexp("fake", "email", emailRe)
	e := newEnv(t, setup{targets: pool, routes: []route.Route{r}, detectors: []privacy.Detector{det}, dispatcher: d})
	return e, fps
}

func requestBytes(req chat.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Text())
		for _, tc := range m.ToolCalls {
			b.WriteString(tc.Function.Arguments)
		}
	}
	return b.String()
}

func drawOutcomes(rt *rapid.T) map[string][]fakeprovider.Outcome {
	errs := []error{nil, provErr(provider.ErrRateLimited, 429), provErr(provider.ErrUpstreamUnavailable, 503)}
	out := map[string][]fakeprovider.Outcome{}
	for _, name := range []string{"local-weak", "cloud-weak", "local-strong", "cloud-strong"} {
		for i := range rapid.IntRange(0, 2).Draw(rt, name+"-fails") {
			if err := rapid.SampledFrom(errs).Draw(rt, fmt.Sprintf("%s-err%d", name, i)); err != nil {
				out[name] = append(out[name], fakeprovider.Fail(err))
			}
		}
	}
	return out
}

func drawPrivateRequest(rt *rapid.T) (chat.Request, []string) {
	values := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{3,8}@example\.com`), 1, 3,
		func(s string) string { return s }).Draw(rt, "values")
	req := userReq("contact " + strings.Join(values, " and ") + " today")
	if rapid.Bool().Draw(rt, "history") {
		req.Messages = append(req.Messages,
			chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{
				ID: "c1", Type: "function",
				Function: chat.FunctionCall{Name: "send", Arguments: `{"to":"` + values[0] + `"}`},
			}}},
			chat.TextMessage(chat.RoleUser, "again for "+values[len(values)-1]))
	}
	if rapid.Bool().Draw(rt, "strong") {
		req = withHeader(req, route.HeaderTier, "strong")
	}
	return req, values
}

// TestPipeline_Chat_MaskNeverLeaksProperty is the F5 DoD end to end with the
// real dispatcher: in mask mode no request a cloud target's provider
// receives contains a finding value, across fallback paths.
func TestPipeline_Chat_MaskNeverLeaksProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		r := privacyRoute(privacy.ModeMask, "")
		r.Rules.AllowClientOverride = true
		r.Fallback = []route.Fallback{{Tier: model.TierStrong}, {Tier: model.TierWeak}}
		e, fps := realPipeline(rt, r, drawOutcomes(rt))
		req, values := drawPrivateRequest(rt)
		_, _ = e.p.Chat(context.Background(), req)
		for _, name := range []string{"cloud-weak", "cloud-strong"} {
			for _, got := range fps[name].Requests() {
				body := requestBytes(got)
				for _, v := range values {
					assert.NotContains(rt, body, v, "cloud target %s received a finding value", name)
				}
				assert.Contains(rt, body, "[[EMAIL_1]]")
			}
		}
		d := e.only(rt)
		for _, a := range d.Attempts {
			assert.Equal(rt, a.Locality == model.LocalityCloud, a.Masked, "attempt %s", a.Target)
		}
	})
}

// TestPipeline_Chat_LocalOnlyProperty: for any request with a finding in
// local_only, every attempt's target is local (real dispatcher).
func TestPipeline_Chat_LocalOnlyProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		r := privacyRoute(privacy.ModeLocalOnly, "")
		r.Rules.AllowClientOverride = true
		r.Fallback = []route.Fallback{{Tier: model.TierStrong}, {Target: "cloud-weak"}, {Target: "cloud-strong"}}
		e, fps := realPipeline(rt, r, drawOutcomes(rt))
		req, _ := drawPrivateRequest(rt)
		_, err := e.p.Chat(context.Background(), req)
		assert.Empty(rt, fps["cloud-weak"].Requests())
		assert.Empty(rt, fps["cloud-strong"].Requests())
		d := e.only(rt)
		for _, a := range d.Attempts {
			assert.Equal(rt, model.LocalityLocal, a.Locality, "attempt %s", a.Target)
		}
		if err != nil {
			var de *dispatch.Error
			assert.ErrorAs(rt, err, &de, "only upstream failures")
		}
	})
}

func TestPipeline_Chat_RealDispatcherSuccess(t *testing.T) {
	t.Parallel()
	r := privacyRoute(privacy.ModeMask, "")
	r.Pool = []string{"cloud-weak", "local-weak"}
	e, fps := realPipeline(t, r, map[string][]fakeprovider.Outcome{
		"cloud-weak": {fakeprovider.Fail(provErr(provider.ErrRateLimited, 429))},
	})
	out, err := e.p.Chat(context.Background(), userReq("mail abc@example.com"))
	require.NoError(t, err)
	assert.Equal(t, "local-weak", out.Target)
	assert.Equal(t, "mail abc@example.com", out.Response.Choices[0].Message.Text(), "local attempt got the original")
	d := e.only(t)
	assert.Equal(t, []string{"privacy:mask", "default:weak", "fallback:429"}, d.Reasons)
	require.Len(t, d.Attempts, 2)
	assert.True(t, d.Attempts[0].Masked)
	assert.False(t, d.Attempts[1].Masked)
	assert.Equal(t, "mail [[EMAIL_1]]", fps["cloud-weak"].Requests()[0].LastUserText())
	assert.Equal(t, map[string]int{"email": 1}, d.Findings)
}
