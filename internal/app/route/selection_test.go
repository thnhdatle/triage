// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

func TestPipeline_Chat_SelectionFilters(t *testing.T) {
	t.Parallel()
	maxOut := 10000
	tests := []struct {
		name      string
		health    healthSet
		edit      func(*chat.Request)
		wantChain []string
		wantTier  model.Tier
		reasons   []string
	}{
		{name: "pool order", wantChain: []string{"local-weak", "cloud-weak"}, wantTier: model.TierWeak},
		{
			name: "unhealthy dropped", health: healthSet{"local-weak": true},
			wantChain: []string{"cloud-weak"}, wantTier: model.TierWeak,
		},
		{
			name: "tools required", edit: func(r *chat.Request) { r.Tools = []chat.Tool{{Type: "function"}} },
			wantChain: []string{"local-weak"}, wantTier: model.TierWeak,
		},
		{
			name: "context window", edit: func(r *chat.Request) { r.MaxTokens = &maxOut },
			wantChain: []string{"cloud-weak"}, wantTier: model.TierWeak,
		},
		{
			name: "vision escalates", wantTier: model.TierStrong, wantChain: []string{"cloud-strong"},
			edit: func(r *chat.Request) {
				r.Messages[1].Content = append(r.Messages[1].Content, chat.Part{Type: chat.PartImage, ImageURL: "x"})
			},
			reasons: []string{"tier:escalate"},
		},
		{
			name: "json_mode escalates", wantTier: model.TierStrong, wantChain: []string{"cloud-strong"},
			edit:    func(r *chat.Request) { r.ResponseFormat = json.RawMessage(`{"type":"json_object"}`) },
			reasons: []string{"tier:escalate"},
		},
		{
			name: "null response_format needs nothing", wantTier: model.TierWeak,
			edit:      func(r *chat.Request) { r.ResponseFormat = json.RawMessage(`null`) },
			wantChain: []string{"local-weak", "cloud-weak"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, setup{health: tc.health})
			req := userReq("hello")
			if tc.edit != nil {
				tc.edit(&req)
			}
			out, err := e.p.Chat(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, tc.wantChain, names(e.disp.lastPlan(t).Chain))
			d := e.only(t)
			assert.Equal(t, tc.wantTier, d.Tier)
			assert.Equal(t, append([]string{"default:weak"}, tc.reasons...), d.Reasons)
			assert.Equal(t, tc.wantChain[0], out.Target)
			assert.Equal(t, tc.wantChain[0]+"-model", out.Model)
		})
	}
}

func TestPipeline_Chat_OnTierUnavailable(t *testing.T) {
	t.Parallel()
	strongOnly := []string{"local-strong", "cloud-strong"}
	weakOnly := []string{"local-weak", "cloud-weak"}
	tests := []struct {
		name      string
		pool      []string
		def       model.Tier
		action    route.TierAction
		wantChain []string
		wantTier  model.Tier
		wantErr   error
		reason    string
	}{
		{
			name: "escalate weak to strong", pool: strongOnly, def: model.TierWeak, action: route.TierEscalate,
			wantChain: strongOnly, wantTier: model.TierStrong, reason: "tier:escalate",
		},
		{
			name: "default action escalates", pool: strongOnly, def: model.TierWeak,
			wantChain: strongOnly, wantTier: model.TierStrong, reason: "tier:escalate",
		},
		{
			name: "escalate never degrades", pool: weakOnly, def: model.TierStrong, action: route.TierEscalate,
			wantErr: route.ErrNoEligibleTarget, wantTier: model.TierStrong,
		},
		{
			name: "degrade strong to weak", pool: weakOnly, def: model.TierStrong, action: route.TierDegrade,
			wantChain: weakOnly, wantTier: model.TierWeak, reason: "tier:degrade",
		},
		{
			name: "degrade never escalates", pool: strongOnly, def: model.TierWeak, action: route.TierDegrade,
			wantErr: route.ErrNoEligibleTarget, wantTier: model.TierWeak,
		},
		{
			name: "fail", pool: strongOnly, def: model.TierWeak, action: route.TierFail,
			wantErr: route.ErrNoEligibleTarget, wantTier: model.TierWeak,
		},
		{
			name: "tier available needs no action", pool: weakOnly, def: model.TierWeak, action: route.TierFail,
			wantChain: weakOnly, wantTier: model.TierWeak,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := baseRoute()
			r.Pool, r.DefaultTier, r.OnTierUnavailable = tc.pool, tc.def, tc.action
			e := newEnv(t, setup{routes: []route.Route{r}})
			out, err := e.p.Chat(context.Background(), userReq("hello"))
			d := e.only(t)
			assert.Equal(t, tc.wantTier, d.Tier)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				code, status := route.Code(err)
				assert.Equal(t, "no_eligible_target", code)
				assert.Equal(t, 503, status)
				assert.Equal(t, "no_eligible_target", d.Error)
				assert.Equal(t, 0, e.disp.calls())
				assert.Empty(t, out.Target)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantChain, names(e.disp.lastPlan(t).Chain))
			want := []string{"default:" + string(tc.def)}
			if tc.reason != "" {
				want = append(want, tc.reason)
			}
			assert.Equal(t, want, d.Reasons)
		})
	}
}

func TestPipeline_Chat_LocalityNeverRelaxed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		health    healthSet
		wantChain []string
		wantErr   bool
	}{
		{name: "escalates within local", health: healthSet{"local-weak": true}, wantChain: []string{"local-strong"}},
		{name: "no local target", health: healthSet{"local-weak": true, "local-strong": true}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := privacyRoute(privacy.ModeLocalOnly, "")
			r.Fallback = []route.Fallback{{Tier: model.TierWeak}, {Target: "cloud-weak"}}
			e := newEnv(t, setup{routes: []route.Route{r}, health: tc.health, detectors: []privacy.Detector{emailDetector()}})
			_, err := e.p.Chat(context.Background(), userReq("mail "+secret))
			if tc.wantErr {
				require.ErrorIs(t, err, privacy.ErrNoLocalTarget)
				code, status := route.Code(err)
				assert.Equal(t, "privacy_no_local_target", code)
				assert.Equal(t, 503, status)
				assert.Equal(t, "privacy_no_local_target", e.only(t).Error)
				assert.Equal(t, 0, e.disp.calls())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantChain, names(e.disp.lastPlan(t).Chain))
		})
	}
}

func TestPipeline_Chat_FallbackChain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		pool      []string
		fallback  []string
		health    healthSet
		wantChain []string
	}{
		{
			name: "tiers in order", fallback: []string{"tier:strong", "tier:weak"},
			wantChain: []string{"local-weak", "cloud-weak", "local-strong", "cloud-strong"},
		},
		{
			name: "target names", fallback: []string{"cloud-strong", "local-weak", "local-strong"},
			wantChain: []string{"local-weak", "cloud-weak", "cloud-strong", "local-strong"},
		},
		{
			name: "target outside pool", pool: []string{"local-weak"}, fallback: []string{"cloud-strong"},
			wantChain: []string{"local-weak", "cloud-strong"},
		},
		{
			name: "tier entries stay in pool", pool: []string{"local-weak"}, fallback: []string{"tier:strong"},
			wantChain: []string{"local-weak"},
		},
		{
			name: "unhealthy fallback dropped", fallback: []string{"cloud-strong", "tier:strong"},
			health: healthSet{"cloud-strong": true}, wantChain: []string{"local-weak", "cloud-weak", "local-strong"},
		},
		{
			name: "embedding target never in chat chain", fallback: []string{"local-embed"},
			wantChain: []string{"local-weak", "cloud-weak"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := baseRoute()
			r.Pool = tc.pool
			for _, s := range tc.fallback {
				f, err := route.ParseFallback(s)
				require.NoError(t, err)
				assert.Equal(t, s, f.String())
				r.Fallback = append(r.Fallback, f)
			}
			e := newEnv(t, setup{routes: []route.Route{r}, health: tc.health})
			_, err := e.p.Chat(context.Background(), userReq("hello"))
			require.NoError(t, err)
			assert.Equal(t, tc.wantChain, names(e.disp.lastPlan(t).Chain))
		})
	}
}

func TestParseFallback_Invalid(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "tier:", "tier:medium"} {
		_, err := route.ParseFallback(s)
		assert.Error(t, err, s)
	}
}

func TestPipeline_Chat_ContextLengthPool(t *testing.T) {
	t.Parallel()
	maxOut := 10000
	e := newEnv(t, setup{health: healthSet{"local-strong": true}})
	req := userReq("hello")
	req.MaxTokens = &maxOut
	req.Tools = []chat.Tool{{Type: "function"}}
	_, err := e.p.Chat(context.Background(), req)
	require.NoError(t, err)
	plan := e.disp.lastPlan(t)
	assert.Equal(t, []string{"cloud-strong"}, names(plan.Chain))
	assert.Equal(t, []string{"local-weak", "cloud-strong"}, names(plan.ContextLengthPool),
		"ignores the window, keeps health and capabilities, sorted by window")
}

func TestPipeline_Chat_RoundRobin(t *testing.T) {
	t.Parallel()
	r := baseRoute()
	r.Fallback = []route.Fallback{{Tier: model.TierStrong}}
	e := newEnv(t, setup{
		routes: []route.Route{r}, opts: route.Options{Selection: route.SelectionOptions{Mode: route.SelectRoundRobin}},
	})
	want := [][]string{
		{"local-weak", "cloud-weak", "local-strong", "cloud-strong"},
		{"cloud-weak", "local-weak", "local-strong", "cloud-strong"},
		{"local-weak", "cloud-weak", "local-strong", "cloud-strong"},
	}
	for i, w := range want {
		_, err := e.p.Chat(context.Background(), userReq("hello"))
		require.NoError(t, err)
		assert.Equal(t, w, names(e.disp.lastPlan(t).Chain), "request %d", i)
	}
}

func TestPipeline_Chat_LeastInflight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{opts: route.Options{Selection: route.SelectionOptions{Mode: route.SelectLeastInflight}}})
	entered, release := make(chan struct{}), make(chan struct{})
	first := true
	e.disp.fn = func(plan dispatch.Plan) (dispatch.Result, error) {
		if first {
			first = false
			close(entered)
			<-release
		}
		return okResult(plan), nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.p.Chat(context.Background(), userReq("hello"))
		done <- err
	}()
	<-entered
	_, err := e.p.Chat(context.Background(), userReq("hello"))
	require.NoError(t, err)
	assert.Equal(t, []string{"cloud-weak", "local-weak"}, names(e.disp.lastPlan(t).Chain))
	close(release)
	require.NoError(t, <-done)
	_, err = e.p.Chat(context.Background(), userReq("hello"))
	require.NoError(t, err)
	assert.Equal(t, []string{"local-weak", "cloud-weak"}, names(e.disp.lastPlan(t).Chain), "counts released")
}

func TestPipeline_Chat_UnknownRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{})
	req := userReq("hello")
	req.Model = "gpt-9"
	out, err := e.p.Chat(context.Background(), req)
	require.ErrorIs(t, err, route.ErrUnknownRoute)
	code, status := route.Code(err)
	assert.Equal(t, "model_not_found", code)
	assert.Equal(t, 404, status)
	d := e.only(t)
	assert.Equal(t, "gpt-9", d.Route)
	assert.Equal(t, "model_not_found", d.Error)
	assert.Equal(t, "gpt-9", out.Route)
	assert.Equal(t, "d-1", out.DecisionID)
	assert.Equal(t, "chat", d.Operation)
	assert.Equal(t, epoch, d.Time)
}

func TestPipeline_Chat_DispatchFailureRecorded(t *testing.T) {
	t.Parallel()
	r := baseRoute()
	r.Fallback = []route.Fallback{{Tier: model.TierStrong}}
	e := newEnv(t, setup{routes: []route.Route{r}})
	rate := &provider.Error{Kind: provider.ErrRateLimited, StatusCode: 429}
	e.disp.fn = func(plan dispatch.Plan) (dispatch.Result, error) {
		attempts := []decision.Attempt{
			{Target: "local-weak", Status: "rate_limited", HTTPStatus: 429},
			{Target: "cloud-weak", Status: "timeout"},
			{Target: "local-strong", Status: "rate_limited", HTTPStatus: 429, CostUSD: 0.25},
		}
		res := dispatch.Result{
			Target: localStrong, Attempts: attempts, Usage: chat.Usage{PromptTokens: 7}, CostUSD: 0.25,
		}
		return res, &dispatch.Error{Last: rate, Attempts: attempts}
	}
	out, err := e.p.Chat(context.Background(), userReq("hello"))
	var de *dispatch.Error
	require.ErrorAs(t, err, &de)
	d := e.only(t)
	assert.Equal(t, "rate_limit_exceeded", d.Error)
	assert.Equal(t, []string{"default:weak", "fallback:429", "fallback:timeout"}, d.Reasons)
	assert.Len(t, d.Attempts, 3)
	assert.Equal(t, chat.Usage{PromptTokens: 7}, d.Usage)
	assert.InDelta(t, 0.25, d.CostUSD, 0)
	assert.Equal(t, model.TierStrong, d.Tier, "final tier is the last attempt's")
	assert.Equal(t, "local-strong", out.Target)
}

func TestPipeline_Chat_UnmaskPartialObserved(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{})
	e.disp.fn = func(plan dispatch.Plan) (dispatch.Result, error) {
		res := okResult(plan)
		res.Unmask = decision.UnmaskPartial
		return res, nil
	}
	_, err := e.p.Chat(context.Background(), userReq("hello"))
	require.NoError(t, err)
	assert.Equal(t, decision.UnmaskPartial, e.only(t).Unmask)
	assert.Len(t, e.obs.kinds(route.EventUnmaskFailure), 1)
}

func TestPipeline_ChatStream_UsesStream(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{})
	s := &sink{}
	out, err := e.p.ChatStream(context.Background(), userReq("hello"), s)
	require.NoError(t, err)
	assert.Equal(t, []string{"local-weak"}, s.started)
	assert.Equal(t, "local-weak", out.Target)
	assert.Equal(t, 1, e.rec.Len())
	assert.Equal(t, "d-1", out.DecisionID)
	assert.Equal(t, routeName, out.Route)
}
