// SPDX-License-Identifier: Apache-2.0

package dispatch_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

func TestNew_Options(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(epoch)
	tests := []struct {
		name    string
		clk     clock.Clock
		ps      map[string]provider.Provider
		opts    dispatch.Options
		wantErr string
	}{
		{name: "defaults", clk: clk},
		{name: "explicit", clk: clk, opts: dispatch.Options{
			MaxAttempts: 5, StreamFailure: dispatch.StreamFailureError,
			Backoff: dispatch.Backoff{Base: time.Second, Max: time.Second},
		}},
		{name: "nil clock", wantErr: "nil clock"},
		{name: "negative attempts", clk: clk, opts: dispatch.Options{MaxAttempts: -1}, wantErr: "negative"},
		{name: "negative timeout", clk: clk, opts: dispatch.Options{AttemptTimeout: -1}, wantErr: "negative"},
		{
			name: "max below base", clk: clk,
			opts:    dispatch.Options{Backoff: dispatch.Backoff{Base: time.Second, Max: time.Millisecond}},
			wantErr: "below base",
		},
		{name: "bad stream failure", clk: clk, opts: dispatch.Options{StreamFailure: "retry"}, wantErr: "stream_failure"},
		{name: "nil provider", clk: clk, ps: map[string]provider.Provider{"a": nil}, wantErr: "nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := dispatch.New(tc.ps, tc.clk, tc.opts)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, d)
		})
	}
}

func TestDispatcher_Backoff_JitteredExponential(t *testing.T) {
	t.Parallel()
	d, err := dispatch.New(nil, clock.NewFake(epoch), dispatch.Options{
		Backoff: dispatch.Backoff{Base: 100 * time.Millisecond, Max: 2 * time.Second},
		Rand:    rand.New(rand.NewPCG(7, 7)),
	})
	require.NoError(t, err)
	tests := []struct {
		n    int
		full time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{5, 1600 * time.Millisecond},
		{6, 2 * time.Second},
		{40, 2 * time.Second},
	}
	for _, tc := range tests {
		seen := map[time.Duration]bool{}
		for range 200 {
			b := d.BackoffForTest(tc.n)
			assert.GreaterOrEqual(t, b, tc.full/2, "n=%d", tc.n)
			assert.LessOrEqual(t, b, tc.full, "n=%d", tc.n)
			seen[b] = true
		}
		assert.Greater(t, len(seen), 1, "n=%d: jitter", tc.n)
	}
	// The default generator is seeded from the clock.
	d2, err := dispatch.New(nil, clock.NewFake(epoch), dispatch.Options{})
	require.NoError(t, err)
	assert.LessOrEqual(t, d2.BackoffForTest(1), dispatch.DefaultBackoffBase)
}

func TestDispatcher_Complete_FallbackByError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		err          error
		wantStatuses []string
		wantErr      error
	}{
		{name: "429", err: errRate, wantStatuses: []string{"a:rate_limited", "b:ok"}},
		{name: "500", err: errServer, wantStatuses: []string{"a:unavailable", "b:ok"}},
		{
			name: "502 plain", err: &provider.Error{Kind: errors.New("x"), StatusCode: 502},
			wantStatuses: []string{"a:server_error", "b:ok"},
		},
		{name: "unavailable", err: errUnavailable, wantStatuses: []string{"a:unavailable", "b:ok"}},
		{name: "upstream timeout", err: errTimeout, wantStatuses: []string{"a:timeout", "b:ok"}},
		{name: "bad request", err: errBad, wantStatuses: []string{"a:bad_request"}, wantErr: provider.ErrBadRequest},
		{
			name: "context length", err: errCtxLen, wantStatuses: []string{"a:context_length"},
			wantErr: provider.ErrContextLength,
		},
		{name: "other error", err: errors.New("boom"), wantStatuses: []string{"a:error"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(fakeprovider.Fail(tc.err))
			b := scripted(fakeprovider.Respond(response("b-up", "hi", chat.Usage{})))
			h := &health{}
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{Health: h})
			res, err := d.Complete(context.Background(), plan(userRequest("hello"), local("a"), local("b")))
			assert.Equal(t, tc.wantStatuses, statuses(res))
			if len(tc.wantStatuses) == 1 {
				var de *dispatch.Error
				require.ErrorAs(t, err, &de)
				assert.Len(t, de.Attempts, 1)
				assert.False(t, de.Started)
				if tc.wantErr != nil {
					require.ErrorIs(t, err, tc.wantErr)
				}
				assert.Empty(t, b.Requests())
				assert.Equal(t, "a", res.Target.Name)
				assert.Equal(t, []string{"a:ok"}, h.all(), "non-retryable answers prove the upstream alive")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "b", res.Target.Name)
			assert.Equal(t, "b-up", res.Model)
			assert.Equal(t, "b-up", res.Response.Model)
			assert.Equal(t, []string{"a:fail", "b:ok"}, h.all())
			require.Len(t, b.Requests(), 1)
			assert.Equal(t, "b-model", b.Requests()[0].Model)
			assert.Equal(t, "a-model", a.Requests()[0].Model)
			if se := provider.StatusCode(tc.err); se != 0 {
				assert.Equal(t, se, res.Attempts[0].HTTPStatus)
			}
		})
	}
}

func TestDispatcher_Complete_ContextLengthFallback(t *testing.T) {
	t.Parallel()
	big := local("big")
	big.ContextWindow = 128000
	small := local("small")
	small.ContextWindow = 4096
	tests := []struct {
		name         string
		enabled      bool
		pool         []model.Target
		bigOutcome   fakeprovider.Outcome
		wantStatuses []string
		wantOK       bool
	}{
		{name: "disabled", pool: []model.Target{big}, wantStatuses: []string{"a:context_length"}},
		{
			name: "larger target", enabled: true, pool: []model.Target{small, big},
			bigOutcome:   fakeprovider.Respond(response("big-up", "ok", chat.Usage{})),
			wantStatuses: []string{"a:context_length", "big:ok"}, wantOK: true,
		},
		{name: "no larger target", enabled: true, pool: []model.Target{small}, wantStatuses: []string{"a:context_length"}},
		{
			name: "only once", enabled: true, pool: []model.Target{big},
			bigOutcome:   fakeprovider.Fail(errCtxLen),
			wantStatuses: []string{"a:context_length", "big:context_length"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(fakeprovider.Fail(errCtxLen))
			bigP := scripted(tc.bigOutcome)
			ps := map[string]provider.Provider{"a": a, "big": bigP, "small": fakeprovider.New()}
			d, _ := newDispatcher(t, ps, dispatch.Options{ContextLengthFallback: tc.enabled})
			p := plan(userRequest("long"), local("a"), local("b"))
			p.ContextLengthPool = tc.pool
			res, err := d.Complete(context.Background(), p)
			assert.Equal(t, tc.wantStatuses, statuses(res))
			if tc.wantOK {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, provider.ErrContextLength)
		})
	}
}

func TestDispatcher_Complete_SingleTargetCyclesHonouringRetryAfter(t *testing.T) {
	t.Parallel()
	ra := 3 * time.Second
	a := scripted(
		fakeprovider.Fail(provErr(provider.ErrRateLimited, 429, ra)),
		fakeprovider.Fail(provErr(provider.ErrRateLimited, 429, ra)),
		fakeprovider.Respond(response("a-up", "third time", chat.Usage{})),
	)
	d, clk := newDispatcher(t, map[string]provider.Provider{"a": a}, dispatch.Options{})
	done := async(func() (dispatch.Result, error) {
		return d.Complete(context.Background(), plan(userRequest("x"), local("a")))
	})
	// total timer, attempt 1 timer, Retry-After wait
	waitWaiters(t, clk, 3)
	assert.Len(t, a.Requests(), 1)
	clk.Advance(ra - time.Millisecond)
	assert.Len(t, a.Requests(), 1, "Retry-After not yet elapsed")
	clk.Advance(time.Millisecond)
	waitWaiters(t, clk, 4)
	clk.Advance(ra)
	o := await(t, done)
	require.NoError(t, o.err)
	assert.Equal(t, []string{"a:rate_limited", "a:rate_limited", "a:ok"}, statuses(o.res))
	assert.Equal(t, "third time", o.res.Response.Choices[0].Message.Text())
}

func TestDispatcher_Complete_RetryAfterAboveMax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		chain        []model.Target
		wantStatuses []string
	}{
		{name: "single target fails at once", chain: []model.Target{local("a")}, wantStatuses: []string{"a:rate_limited"}},
		{
			name: "moves to next target", chain: []model.Target{local("a"), local("b")},
			wantStatuses: []string{"a:rate_limited", "b:ok"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(fakeprovider.Fail(provErr(provider.ErrRateLimited, 429, time.Minute)))
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": fakeprovider.New()}, dispatch.Options{})
			res, err := d.Complete(context.Background(), plan(userRequest("x"), tc.chain...))
			assert.Equal(t, tc.wantStatuses, statuses(res))
			if len(tc.chain) == 1 {
				require.ErrorIs(t, err, provider.ErrRateLimited)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestDispatcher_Complete_BackoffUntilMaxAttempts(t *testing.T) {
	t.Parallel()
	a := scripted(
		fakeprovider.Fail(errServer), fakeprovider.Fail(errServer), fakeprovider.Fail(errServer),
		fakeprovider.Respond(response("never", "", chat.Usage{})),
	)
	d, clk := newDispatcher(t, map[string]provider.Provider{"a": a}, dispatch.Options{})
	done := async(func() (dispatch.Result, error) {
		return d.Complete(context.Background(), plan(userRequest("x"), local("a")))
	})
	waitWaiters(t, clk, 3) // total, attempt 1, backoff 1 (<= 100ms)
	clk.Advance(100 * time.Millisecond)
	waitWaiters(t, clk, 4) // + attempt 2 timer, backoff 2 (<= 200ms); backoff 1 fired
	clk.Advance(200 * time.Millisecond)
	o := await(t, done)
	require.ErrorIs(t, o.err, provider.ErrUpstreamUnavailable)
	assert.Equal(t, []string{"a:unavailable", "a:unavailable", "a:unavailable"}, statuses(o.res))
	assert.Len(t, a.Requests(), 3)
}

func TestDispatcher_Complete_AttemptTimeout(t *testing.T) {
	t.Parallel()
	slow := newBlocking()
	fast := fakeprovider.New()
	h := &health{}
	d, clk := newDispatcher(t, map[string]provider.Provider{"slow": slow, "fast": fast}, dispatch.Options{Health: h})
	slowT := local("slow")
	slowT.AttemptTimeout = 5 * time.Second
	done := async(func() (dispatch.Result, error) {
		return d.Complete(context.Background(), plan(userRequest("echo me"), slowT, local("fast")))
	})
	waitEntered(t, slow)
	clk.Advance(5*time.Second - time.Nanosecond)
	assert.Empty(t, fast.Requests())
	clk.Advance(time.Nanosecond)
	o := await(t, done)
	require.NoError(t, o.err)
	assert.Equal(t, []string{"slow:timeout", "fast:ok"}, statuses(o.res))
	assert.Equal(t, 5*time.Second, o.res.Attempts[0].Latency)
	assert.Equal(t, "echo me", o.res.Response.Choices[0].Message.Text())
	assert.Equal(t, []string{"slow:fail", "fast:ok"}, h.all())
}

func TestDispatcher_Complete_TotalTimeout(t *testing.T) {
	t.Parallel()
	slow := newBlocking()
	h := &health{}
	d, clk := newDispatcher(t, map[string]provider.Provider{"slow": slow},
		dispatch.Options{TotalTimeout: 10 * time.Second, Health: h})
	done := async(func() (dispatch.Result, error) {
		return d.Complete(context.Background(), plan(userRequest("x"), local("slow")))
	})
	waitEntered(t, slow)
	clk.Advance(10 * time.Second)
	o := await(t, done)
	require.ErrorIs(t, o.err, dispatch.ErrTotalTimeout)
	require.ErrorIs(t, o.err, provider.ErrTimeout)
	assert.Equal(t, []string{"slow:timeout"}, statuses(o.res))
	assert.Empty(t, h.all(), "the total timeout is not the target's fault")
}

func TestDispatcher_Complete_TotalTimeoutDuringBackoff(t *testing.T) {
	t.Parallel()
	a := scripted(fakeprovider.Fail(provErr(provider.ErrRateLimited, 429, 5*time.Second)))
	d, clk := newDispatcher(t, map[string]provider.Provider{"a": a}, dispatch.Options{TotalTimeout: 2 * time.Second})
	done := async(func() (dispatch.Result, error) {
		return d.Complete(context.Background(), plan(userRequest("x"), local("a")))
	})
	waitWaiters(t, clk, 3)
	clk.Advance(2 * time.Second)
	o := await(t, done)
	require.ErrorIs(t, o.err, dispatch.ErrTotalTimeout)
	assert.Len(t, a.Requests(), 1)
}

func TestDispatcher_Complete_ContextCanceled(t *testing.T) {
	t.Parallel()
	slow := newBlocking()
	d, _ := newDispatcher(t, map[string]provider.Provider{"slow": slow, "b": fakeprovider.New()}, dispatch.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := async(func() (dispatch.Result, error) {
		return d.Complete(ctx, plan(userRequest("x"), local("slow"), local("b")))
	})
	waitEntered(t, slow)
	cancel()
	o := await(t, done)
	require.ErrorIs(t, o.err, context.Canceled)
	assert.Equal(t, []string{"slow:canceled"}, statuses(o.res))
}

func TestDispatcher_Complete_Localities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		localities   model.LocalitySet
		wantStatuses []string
		wantErr      error
	}{
		{name: "local only skips cloud", localities: model.LocalOnly(), wantStatuses: []string{"l:ok"}},
		{name: "nothing allowed", localities: model.LocalitySet{}, wantErr: dispatch.ErrNoTarget},
		{name: "all", localities: model.AllLocalities(), wantStatuses: []string{"c:ok"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fakeprovider.New()
			l := fakeprovider.New()
			d, _ := newDispatcher(t, map[string]provider.Provider{"c": c, "l": l}, dispatch.Options{})
			p := plan(userRequest("x"), cloud("c"), local("l"))
			p.Localities = tc.localities
			res, err := d.Complete(context.Background(), p)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, res.Attempts)
				assert.Empty(t, c.Requests())
				assert.Empty(t, l.Requests())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatuses, statuses(res))
			if !tc.localities.Cloud {
				assert.Empty(t, c.Requests())
			}
		})
	}
}

func TestDispatcher_Complete_MaskPerAttempt(t *testing.T) {
	t.Parallel()
	c1 := scripted(fakeprovider.Fail(errServer))
	l := scripted(fakeprovider.Fail(errServer))
	c2 := fakeprovider.New() // echoes the (masked) last user text
	d, _ := newDispatcher(t, map[string]provider.Provider{"c1": c1, "l": l, "c2": c2}, dispatch.Options{})
	p := maskedPlan(t, cloud("c1"), local("l"), cloud("c2"))
	res, err := d.Complete(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, []string{"c1:unavailable", "l:unavailable", "c2:ok"}, statuses(res))
	masked := []bool{res.Attempts[0].Masked, res.Attempts[1].Masked, res.Attempts[2].Masked}
	assert.Equal(t, []bool{true, false, true}, masked)
	for _, prov := range []*fakeprovider.Provider{c1, c2} {
		got := requestText(prov.Requests()[0])
		assert.NotContains(t, got, secret)
		assert.Contains(t, got, "[[EMAIL_1]]")
	}
	assert.Contains(t, requestText(l.Requests()[0]), secret, "local attempts get the original")
	assert.Equal(t, "mail "+secret+" please", res.Response.Choices[0].Message.Text())
	assert.Equal(t, decision.UnmaskOK, res.Unmask)
	assert.Equal(t, "auto", p.Request.Model, "the plan request is not modified")
}

func TestDispatcher_Complete_UnmaskStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target model.Target
		text   string
		want   decision.UnmaskStatus
		output string
	}{
		{name: "cloud restored", target: cloud("c"), text: "to [[EMAIL_1]]", want: decision.UnmaskOK, output: "to " + secret},
		{
			name: "cloud partial", target: cloud("c"), text: "to [[EMAIL_9]]",
			want: decision.UnmaskPartial, output: "to [[EMAIL_9]]",
		},
		{
			name: "local not masked", target: local("c"), text: "to [[EMAIL_9]]",
			want: decision.UnmaskNone, output: "to [[EMAIL_9]]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := scripted(fakeprovider.Respond(response("up", tc.text, chat.Usage{})))
			d, _ := newDispatcher(t, map[string]provider.Provider{"c": c}, dispatch.Options{})
			res, err := d.Complete(context.Background(), maskedPlan(t, tc.target))
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Unmask)
			assert.Equal(t, tc.output, res.Response.Choices[0].Message.Text())
		})
	}
}

func TestDispatcher_Complete_RefusedTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		findingValue string
		chain        []model.Target
		wantStatuses []string
		wantErr      error
	}{
		{
			// The value "EMAIL" reappears inside its own placeholder, so the
			// masked request leaks and every cloud target is refused.
			name: "mask leak", findingValue: "EMAIL", chain: []model.Target{cloud("c"), cloud("c2"), local("l")},
			wantStatuses: []string{"c:mask_leak", "l:ok"},
		},
		{
			name: "mask leak without local", findingValue: "EMAIL", chain: []model.Target{cloud("c")},
			wantStatuses: []string{"c:mask_leak"}, wantErr: dispatch.ErrMaskLeak,
		},
		{
			name: "unknown provider", findingValue: secret,
			chain:        []model.Target{{Name: "x", Provider: "missing", Locality: model.LocalityLocal}, local("l")},
			wantStatuses: []string{"x:unknown_provider", "l:ok"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fakeprovider.New()
			l := fakeprovider.New()
			d, _ := newDispatcher(t, map[string]provider.Provider{"c": c, "c2": c, "l": l}, dispatch.Options{MaxAttempts: 1})
			req := userRequest("value " + tc.findingValue)
			p := plan(req, tc.chain...)
			p.Mapping = newMapping(req, "email", tc.findingValue)
			res, err := d.Complete(context.Background(), p)
			assert.Equal(t, tc.wantStatuses, statuses(res))
			assert.Empty(t, c.Requests(), "refused targets are never sent to")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, l.Requests(), 1, "a refusal does not use the attempt budget")
		})
	}
}

func TestDispatcher_Complete_UsageAndCost(t *testing.T) {
	t.Parallel()
	a := scripted(fakeprovider.Fail(errRate))
	u := chat.Usage{PromptTokens: 1000, CompletionTokens: 500, CachedPromptTokens: 400}
	b := scripted(fakeprovider.Respond(response("b-up", "ok", u)))
	d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{})
	bt := cloud("b")
	bt.Pricing = model.Pricing{InputPerMTok: 1, OutputPerMTok: 2, CachedInputPerMTok: 0.5}
	res, err := d.Complete(context.Background(), plan(userRequest("x"), local("a"), bt))
	require.NoError(t, err)
	// (600*1 + 400*0.5 + 500*2) / 1e6
	assert.InDelta(t, 0.0018, res.Attempts[1].CostUSD, 1e-12)
	assert.InDelta(t, 0.0018, res.CostUSD, 1e-12)
	assert.Zero(t, res.Attempts[0].CostUSD)
	assert.Equal(t, u, res.Usage)
	assert.Equal(t, u, res.Response.Usage)
	assert.Equal(t, u, res.Attempts[1].Usage)
	assert.Equal(t, model.LocalityCloud, res.Attempts[1].Locality)
	assert.Equal(t, "b-model", res.Attempts[1].Model)
}

func TestError_Error(t *testing.T) {
	t.Parallel()
	e := &dispatch.Error{Last: errRate, Attempts: make([]decision.Attempt, 2)}
	assert.True(t, strings.HasPrefix(e.Error(), "dispatch: 2 attempt(s): provider: rate limited"))
	require.ErrorIs(t, e, provider.ErrRateLimited)
}
