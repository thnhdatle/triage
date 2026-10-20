// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/testutil"
	"github.com/thnhdatle/triage/internal/testutil/fakestrategy"
)

func strategyRoute(name string, threshold float64) route.Route {
	r := baseRoute()
	r.Strategy = name
	r.Threshold = difficulty.Threshold(threshold)
	return r
}

type rejectedErr struct{}

func (rejectedErr) Error() string  { return "queue full" }
func (rejectedErr) Rejected() bool { return true }

func TestPipeline_Chat_StrategyScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		p          float64
		threshold  float64
		wantTier   model.Tier
		wantReason string
	}{
		{name: "above", p: 0.71, threshold: 0.62, wantTier: model.TierStrong, wantReason: "strategy:bert p=0.71>=0.62"},
		{name: "equal", p: 0.62, threshold: 0.62, wantTier: model.TierStrong, wantReason: "strategy:bert p=0.62>=0.62"},
		{name: "below", p: 0.3, threshold: 0.62, wantTier: model.TierWeak, wantReason: "strategy:bert p=0.30<0.62"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := fakestrategy.New("bert", func(context.Context, chat.Request) (difficulty.Score, error) {
				return difficulty.Score{P: tc.p, Variant: "int8"}, nil
			})
			e := newEnv(t, setup{
				routes: []route.Route{strategyRoute("bert", tc.threshold)}, strategies: map[string]difficulty.Strategy{"bert": s},
			})
			_, err := e.p.Chat(context.Background(), userReq("hello"))
			require.NoError(t, err)
			d := e.only(t)
			assert.Equal(t, tc.wantTier, d.Tier)
			assert.Equal(t, []string{tc.wantReason}, d.Reasons)
			require.NotNil(t, d.Score)
			assert.InDelta(t, tc.p, *d.Score, 0)
			require.NotNil(t, d.Threshold)
			assert.InDelta(t, tc.threshold, *d.Threshold, 0)
			assert.Equal(t, "bert", d.Strategy)
			assert.Equal(t, "int8", d.Variant)
			assert.InDelta(t, 0.0, d.ScoringCostUSD, 0)
			plan := e.disp.lastPlan(t)
			assert.Equal(t, tc.wantTier, plan.Chain[0].Tier)
			assert.Len(t, e.obs.kinds(route.EventScoring), 1)
			tiers := e.obs.kinds(route.EventTier)
			require.Len(t, tiers, 1)
			assert.Equal(t, tc.wantTier, tiers[0].Tier)
		})
	}
}

func TestPipeline_Chat_StrategyError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		p          float64
		onError    model.Tier
		wantTier   model.Tier
		wantReason string
		rejected   bool
	}{
		{
			name: "error default strong", err: errors.New("boom"), wantTier: model.TierStrong,
			wantReason: "strategy:bert error->strong(error)",
		},
		{
			name: "error on_error weak", err: errors.New("boom"), onError: model.TierWeak, wantTier: model.TierWeak,
			wantReason: "strategy:bert error->weak(error)",
		},
		{
			name: "no scored text", err: fmt.Errorf("x: %w", difficulty.ErrNoScoredText), wantTier: model.TierStrong,
			wantReason: "strategy:bert error->strong(no_scored_text)",
		},
		{
			name: "rejected", err: fmt.Errorf("x: %w", rejectedErr{}), wantTier: model.TierStrong,
			wantReason: "strategy:bert error->strong(rejected)", rejected: true,
		},
		{
			name: "own deadline", err: context.DeadlineExceeded, wantTier: model.TierStrong,
			wantReason: "strategy:bert error->strong(timeout)", rejected: true,
		},
		{name: "score above one", p: 1.5, wantTier: model.TierStrong, wantReason: "strategy:bert error->strong(error)"},
		{name: "score NaN", p: math.NaN(), wantTier: model.TierStrong, wantReason: "strategy:bert error->strong(error)"},
		{name: "score negative", p: -0.1, wantTier: model.TierStrong, wantReason: "strategy:bert error->strong(error)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := fakestrategy.New("bert", func(context.Context, chat.Request) (difficulty.Score, error) {
				return difficulty.Score{P: tc.p}, tc.err
			})
			e := newEnv(t, setup{
				routes:     []route.Route{strategyRoute("bert", 0.5)},
				strategies: map[string]difficulty.Strategy{"bert": s},
				opts:       route.Options{Difficulty: route.DifficultyOptions{OnError: tc.onError}},
			})
			_, err := e.p.Chat(context.Background(), userReq("hello"))
			require.NoError(t, err)
			d := e.only(t)
			assert.Equal(t, tc.wantTier, d.Tier)
			assert.Equal(t, []string{tc.wantReason}, d.Reasons)
			assert.Nil(t, d.Score)
			require.NotNil(t, d.Threshold)
			assert.Equal(t, "bert", d.Strategy)
			assert.Len(t, e.obs.kinds(route.EventScoringRejected), map[bool]int{true: 1}[tc.rejected])
		})
	}
}

func TestPipeline_Chat_StrategyTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		strategy string
		timeouts map[string]time.Duration
		want     time.Duration
	}{
		{name: "bert default", strategy: "bert", want: route.DefaultBERTTimeout},
		{name: "mf default", strategy: "mf", want: route.DefaultMFTimeout},
		{name: "length default", strategy: "length", want: route.DefaultStrategyTimeout},
		{
			name: "configured", strategy: "random",
			timeouts: map[string]time.Duration{"random": 7 * time.Millisecond}, want: 7 * time.Millisecond,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := fakestrategy.Blocking(tc.strategy)
			strategies := map[string]difficulty.Strategy{"length": fakestrategy.Fixed("length", 0)}
			strategies[tc.strategy] = s
			e := newEnv(t, setup{
				routes:     []route.Route{strategyRoute(tc.strategy, 0.5)},
				strategies: strategies,
				opts: route.Options{Difficulty: route.DifficultyOptions{
					OnError: model.TierWeak, Timeouts: tc.timeouts, MFFallback: "length",
				}},
			})
			done := make(chan error, 1)
			go func() {
				_, err := e.p.Chat(context.Background(), userReq("hello"))
				done <- err
			}()
			<-s.Entered()
			testutil.Eventually(t, func() bool { return e.clk.Waiters() == 1 }, waitLimit)
			e.clk.Advance(tc.want - time.Nanosecond)
			select {
			case <-done:
				t.Fatal("strategy timed out early")
			default:
			}
			e.clk.Advance(time.Nanosecond)
			require.NoError(t, <-done)
			d := e.only(t)
			assert.Equal(t, model.TierWeak, d.Tier)
			assert.Equal(t, []string{"strategy:" + tc.strategy + " error->weak(timeout)"}, d.Reasons)
			assert.Equal(t, tc.want, d.ScoringLatency)
			rej := e.obs.kinds(route.EventScoringRejected)
			require.Len(t, rej, 1)
			assert.Equal(t, "timeout", rej[0].Reason)
			assert.Equal(t, tc.strategy, rej[0].Strategy)
		})
	}
}

func TestPipeline_Chat_ScoringLatencyFromClock(t *testing.T) {
	t.Parallel()
	var e env
	s := fakestrategy.New("bert", func(context.Context, chat.Request) (difficulty.Score, error) {
		e.clk.Advance(40 * time.Millisecond)
		return difficulty.Score{P: 0.1}, nil
	})
	e = newEnv(t, setup{
		routes: []route.Route{strategyRoute("bert", 0.5)}, strategies: map[string]difficulty.Strategy{"bert": s},
	})
	_, err := e.p.Chat(context.Background(), userReq("hello"))
	require.NoError(t, err)
	d := e.only(t)
	assert.Equal(t, 40*time.Millisecond, d.ScoringLatency)
	sc := e.obs.kinds(route.EventScoring)
	require.Len(t, sc, 1)
	assert.Equal(t, 40*time.Millisecond, sc[0].Duration)
	oh := e.obs.kinds(route.EventRouteOverhead)
	require.Len(t, oh, 1)
	assert.Equal(t, 40*time.Millisecond, oh[0].Duration)
}

func TestPipeline_Chat_StrategyNoneDefaultTier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		def  model.Tier
		want model.Tier
	}{
		{name: "unset is weak", want: model.TierWeak},
		{name: "strong", def: model.TierStrong, want: model.TierStrong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := baseRoute()
			r.Strategy = ""
			r.DefaultTier = tc.def
			e := newEnv(t, setup{routes: []route.Route{r}})
			_, err := e.p.Chat(context.Background(), userReq("hello"))
			require.NoError(t, err)
			d := e.only(t)
			assert.Equal(t, tc.want, d.Tier)
			assert.Equal(t, []string{"default:" + string(tc.want)}, d.Reasons)
			assert.Nil(t, d.Threshold)
			assert.Empty(t, d.Strategy)
		})
	}
}

func TestPipeline_Chat_ScoringCost(t *testing.T) {
	t.Parallel()
	targets := allTargets()
	targets[4].Pricing.InputPerMTok = 0.02
	tests := []struct {
		name     string
		costTo   string
		tokens   int
		wantCost float64
	}{
		{name: "priced", costTo: "cloud-embed", tokens: 1_000_000, wantCost: 0.02},
		{name: "cache hit", costTo: "cloud-embed", tokens: 0, wantCost: 0},
		{name: "no cost target", costTo: "", tokens: 1_000_000, wantCost: 0},
		{name: "unknown cost target", costTo: "nope", tokens: 1_000_000, wantCost: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := fakestrategy.New("bert", func(context.Context, chat.Request) (difficulty.Score, error) {
				return difficulty.Score{P: 0.9, EmbeddingInputTokens: tc.tokens}, nil
			})
			e := newEnv(t, setup{
				targets: targets, routes: []route.Route{strategyRoute("bert", 0.5)},
				strategies: map[string]difficulty.Strategy{"bert": s},
				opts:       route.Options{Difficulty: route.DifficultyOptions{MFCostTarget: tc.costTo}},
			})
			_, err := e.p.Chat(context.Background(), userReq("hello"))
			require.NoError(t, err)
			d := e.only(t)
			assert.InDelta(t, tc.wantCost, d.ScoringCostUSD, 1e-12)
			assert.InDelta(t, 0.5+tc.wantCost, d.CostUSD, 1e-12)
			assert.Empty(t, e.obs.kinds(route.EventMFExternalEmbedding), "only mf counts external embeddings")
		})
	}
}

func TestPipeline_Chat_StrategyGetsOriginalRequest(t *testing.T) {
	t.Parallel()
	var got chat.Request
	s := fakestrategy.New("bert", func(_ context.Context, req chat.Request) (difficulty.Score, error) {
		got = req
		return difficulty.Score{P: 0.1}, nil
	})
	e := newEnv(t, setup{
		routes: []route.Route{strategyRoute("bert", 0.5)}, strategies: map[string]difficulty.Strategy{"bert": s},
	})
	req := userReq("mail " + secret)
	req.ID = ""
	_, err := e.p.Chat(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "mail "+secret, got.LastUserText())
	assert.Equal(t, "d-1", got.ID, "an empty request id takes the decision id")
	assert.Equal(t, decision.Decision{}.Error, e.only(t).Error)
}
