// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil/fakerecorder"
	"github.com/thnhdatle/triage/internal/testutil/fakestrategy"
)

func validDeps() route.Deps {
	return route.Deps{
		Targets:    allTargets(),
		Routes:     []route.Route{baseRoute()},
		Strategies: map[string]difficulty.Strategy{"bert": fakestrategy.Fixed("bert", 0), "mf": fakestrategy.Fixed("mf", 0)},
		Dispatcher: &fakeDispatcher{},
		Recorder:   fakerecorder.New(),
		Clock:      clock.NewFake(epoch),
		NewID:      func() string { return "id" },
	}
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()
	withRoute := func(edit func(*route.Route)) func(*route.Deps, *route.Options) {
		return func(d *route.Deps, _ *route.Options) {
			r := baseRoute()
			edit(&r)
			d.Routes = []route.Route{r}
		}
	}
	tests := []struct {
		name string
		edit func(*route.Deps, *route.Options)
		want string
	}{
		{name: "nil dispatcher", edit: func(d *route.Deps, _ *route.Options) { d.Dispatcher = nil }, want: "nil dispatcher"},
		{name: "nil recorder", edit: func(d *route.Deps, _ *route.Options) { d.Recorder = nil }, want: "nil recorder"},
		{name: "nil clock", edit: func(d *route.Deps, _ *route.Options) { d.Clock = nil }, want: "nil clock"},
		{name: "nil ids", edit: func(d *route.Deps, _ *route.Options) { d.NewID = nil }, want: "nil id generator"},
		{
			name: "on_error", edit: func(_ *route.Deps, o *route.Options) { o.Difficulty.OnError = "medium" },
			want: "difficulty.on_error",
		},
		{
			name: "selection mode", edit: func(_ *route.Deps, o *route.Options) { o.Selection.Mode = "random" },
			want: "selection.mode",
		},
		{
			name: "on_oversize", edit: func(_ *route.Deps, o *route.Options) { o.Privacy.OnOversize = "drop" },
			want: "on_oversize",
		},
		{
			name: "empty target name", edit: func(d *route.Deps, _ *route.Options) { d.Targets = model.Pool{{}} },
			want: "empty name",
		},
		{name: "duplicate target", edit: func(d *route.Deps, _ *route.Options) {
			d.Targets = model.Pool{localWeak, localWeak}
		}, want: "duplicate target"},
		{name: "duplicate route", edit: func(d *route.Deps, _ *route.Options) {
			d.Routes = []route.Route{baseRoute(), baseRoute()}
		}, want: "duplicate route"},
		{name: "empty route name", edit: withRoute(func(r *route.Route) { r.Name = "" }), want: "empty name"},
		{name: "privacy mode", edit: withRoute(func(r *route.Route) { r.Privacy.Mode = "" }), want: "privacy mode"},
		{
			name: "max client mode", edit: withRoute(func(r *route.Route) { r.Privacy.MaxClientMode = "all" }),
			want: "max_client_mode",
		},
		{name: "default tier", edit: withRoute(func(r *route.Route) { r.DefaultTier = "x" }), want: "default_tier"},
		{
			name: "tier action", edit: withRoute(func(r *route.Route) { r.OnTierUnavailable = "x" }),
			want: "on_tier_unavailable",
		},
		{name: "threshold", edit: withRoute(func(r *route.Route) { r.Threshold = 1.5 }), want: "threshold"},
		{name: "rule without condition", edit: withRoute(func(r *route.Route) {
			r.Rules.Match = []route.Rule{{Tier: model.TierWeak}}
		}), want: "rule 0"},
		{name: "rule with both", edit: withRoute(func(r *route.Route) {
			r.Rules.Match = []route.Rule{{MinPromptTokens: 1, LastUserRegex: regexp.MustCompile("x"), Tier: model.TierWeak}}
		}), want: "rule 0"},
		{name: "negative rule", edit: withRoute(func(r *route.Route) {
			r.Rules.Match = []route.Rule{{MinPromptTokens: -1, LastUserRegex: regexp.MustCompile("x"), Tier: model.TierWeak}}
		}), want: "rule 0"},
		{name: "rule tier", edit: withRoute(func(r *route.Route) {
			r.Rules.Match = []route.Rule{{MinPromptTokens: 1}}
		}), want: "rule 0: unknown tier"},
		{
			name: "fallback empty", edit: withRoute(func(r *route.Route) { r.Fallback = []route.Fallback{{}} }),
			want: "fallback 0",
		},
		{name: "fallback both", edit: withRoute(func(r *route.Route) {
			r.Fallback = []route.Fallback{{Tier: model.TierWeak, Target: "local-weak"}}
		}), want: "fallback 0"},
		{name: "fallback bad tier", edit: withRoute(func(r *route.Route) {
			r.Fallback = []route.Fallback{{Tier: "mid"}}
		}), want: "fallback 0"},
		{name: "fallback unknown target", edit: withRoute(func(r *route.Route) {
			r.Fallback = []route.Fallback{{Target: "nope"}}
		}), want: "unknown fallback target"},
		{
			name: "unknown pool target", edit: withRoute(func(r *route.Route) { r.Pool = []string{"nope"} }),
			want: "unknown pool target",
		},
		{
			name: "unknown strategy", edit: withRoute(func(r *route.Route) { r.Strategy = "magic" }),
			want: "unknown strategy",
		},
		{
			name: "mf without fallback", edit: withRoute(func(r *route.Route) { r.Strategy = "mf" }),
			want: "difficulty.mf.fallback",
		},
		{name: "mf falling back to mf", edit: func(d *route.Deps, o *route.Options) {
			r := baseRoute()
			r.Strategy = "mf"
			d.Routes = []route.Route{r}
			o.Difficulty.MFFallback = "mf"
		}, want: "difficulty.mf.fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, o := validDeps(), route.Options{}
			tc.edit(&d, &o)
			p, err := route.New(d, o)
			require.Error(t, err)
			assert.Nil(t, p)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNew_Defaults(t *testing.T) {
	t.Parallel()
	d := validDeps()
	d.Observer = nil
	d.Health = nil
	p, err := route.New(d, route.Options{})
	require.NoError(t, err)
	out, err := p.Chat(context.Background(), userReq("hello"))
	require.NoError(t, err)
	assert.Equal(t, "local-weak", out.Target)
	route.NopObserver{}.Observe(route.Event{Kind: route.EventTier})
}

func TestCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err    error
		code   string
		status int
	}{
		{nil, "", 200},
		{fmt.Errorf("x: %w", route.ErrUnknownRoute), "model_not_found", 404},
		{privacy.ErrModeNotAllowed, "privacy_mode_not_allowed", 400},
		{privacy.ErrScanLimit, "privacy_scan_limit", 413},
		{privacy.ErrNoLocalTarget, "privacy_no_local_target", 503},
		{route.ErrNoEligibleTarget, "no_eligible_target", 503},
		{&dispatch.Error{Last: dispatch.ErrNoTarget}, "no_eligible_target", 503},
		{&dispatch.Error{Last: dispatch.ErrMaskLeak}, "no_eligible_target", 503},
		{&dispatch.Error{Last: dispatch.ErrRescan}, "no_eligible_target", 503},
		{dispatch.ErrUnknownProvider, "no_eligible_target", 503},
		{context.Canceled, "request_canceled", 499},
		{provErr(provider.ErrContextLength, 400), "context_length_exceeded", 400},
		{provErr(provider.ErrBadRequest, 400), "upstream_bad_request", 400},
		{&dispatch.Error{Last: provErr(provider.ErrRateLimited, 429)}, "rate_limit_exceeded", 429},
		{dispatch.ErrTotalTimeout, "upstream_timeout", 504},
		{context.DeadlineExceeded, "upstream_timeout", 504},
		{provErr(provider.ErrUpstreamUnavailable, 0), "upstream_unavailable", 502},
		{&provider.Error{Kind: errors.New("odd"), StatusCode: 500}, "upstream_unavailable", 502},
		{errors.New("other"), "internal_error", 500},
	}
	for _, tc := range tests {
		code, status := route.Code(tc.err)
		assert.Equal(t, tc.code, code, "%v", tc.err)
		assert.Equal(t, tc.status, status, "%v", tc.err)
	}
}

func TestTierAction_Valid(t *testing.T) {
	t.Parallel()
	for _, a := range []route.TierAction{route.TierEscalate, route.TierDegrade, route.TierFail} {
		assert.True(t, a.Valid())
	}
	assert.False(t, route.TierAction("x").Valid())
	assert.False(t, route.SelectionMode("x").Valid())
}

func TestPipeline_Embeddings_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		req     route.EmbeddingsRequest
		mode    privacy.Mode
		max     int
		want    error
		wantErr string
	}{
		{
			name: "unknown route", req: route.EmbeddingsRequest{Request: provider.EmbedRequest{Model: "x"}},
			mode: privacy.ModeOff, want: route.ErrUnknownRoute, wantErr: "model_not_found",
		},
		{
			name: "mode not allowed", mode: privacy.ModeOff, want: privacy.ErrModeNotAllowed,
			req: route.EmbeddingsRequest{
				Headers: map[string]string{route.HeaderPrivacy: "bogus"}, Request: embedReq("x").Request,
			}, wantErr: "privacy_mode_not_allowed",
		},
		{
			name: "scan limit", mode: privacy.ModeMask, max: 2, req: embedReq("too long"), want: privacy.ErrScanLimit,
			wantErr: "privacy_scan_limit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, setup{
				targets: embedTargets(), routes: []route.Route{embedRoute(tc.mode, "cloud-embed")},
				embedders: map[string]route.Embedder{"cloud-embed": &scriptedEmbedder{}},
				opts: route.Options{Privacy: route.PrivacyOptions{
					MaxScanBytes: tc.max, OnOversize: privacy.OversizeReject,
				}},
			})
			_, err := e.p.Embeddings(context.Background(), tc.req)
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.wantErr, e.only(t).Error)
		})
	}
}

func TestPipeline_Embeddings_AttemptStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want string
	}{
		{provErr(provider.ErrTimeout, 0), "timeout"},
		{provErr(provider.ErrContextLength, 400), "context_length"},
		{provErr(provider.ErrUpstreamUnavailable, 502), "unavailable"},
		{&provider.Error{Kind: errors.New("odd"), StatusCode: 500}, "server_error"},
		{errors.New("other"), "error"},
		{context.Canceled, "canceled"},
	}
	for _, tc := range tests {
		e := newEnv(t, setup{
			targets: embedTargets(), routes: []route.Route{embedRoute(privacy.ModeOff, "cloud-embed")},
			embedders: map[string]route.Embedder{"cloud-embed": &scriptedEmbedder{errs: []error{tc.err}}},
		})
		_, err := e.p.Embeddings(context.Background(), embedReq("x"))
		require.Error(t, err)
		d := e.only(t)
		require.Len(t, d.Attempts, 1)
		assert.Equal(t, tc.want, d.Attempts[0].Status, "%v", tc.err)
		assert.Equal(t, provider.StatusCode(tc.err), d.Attempts[0].HTTPStatus)
	}
}

func TestPipeline_Embeddings_RoundRobin(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{
		targets: embedTargets(), routes: []route.Route{embedRoute(privacy.ModeOff, "cloud-embed", "local-embed")},
		embedders: map[string]route.Embedder{"cloud-embed": &scriptedEmbedder{}, "local-embed": &scriptedEmbedder{}},
		opts:      route.Options{Selection: route.SelectionOptions{Mode: route.SelectRoundRobin}},
	})
	got := make([]string, 0, 3)
	for range 3 {
		out, err := e.p.Embeddings(context.Background(), embedReq("x"))
		require.NoError(t, err)
		got = append(got, out.Target)
	}
	assert.Equal(t, []string{"cloud-embed", "local-embed", "cloud-embed"}, got)
}
