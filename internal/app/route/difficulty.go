// SPDX-License-Identifier: Apache-2.0

package route

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

const (
	CauseTimeout      = "timeout"
	CauseRejected     = "rejected"
	CauseNoScoredText = "no_scored_text"
	CauseError        = "error"
)

const reasonDefault = "default:"

const causePrivacy = "privacy"

var errScoreTimeout = errors.New("route: strategy timeout")

type Rejection interface {
	Rejected() bool
}

func (r *request) difficultyStage(ctx context.Context) {
	cr := r.route
	if cr.strategy == nil {
		r.tier = cr.DefaultTier
		r.dec.AddReason(reasonDefault + string(cr.DefaultTier))
		return
	}
	s, name := cr.strategy, cr.Strategy
	if name == StrategyMF && r.steersMF() {
		s, name = cr.mfFallback, r.p.opts.Difficulty.MFFallback
		r.dec.AddReason(decision.ReasonStrategyFallback(StrategyMF, name, causePrivacy))
	}
	threshold := float64(cr.Threshold)
	r.dec.Strategy = name
	r.dec.Threshold = &threshold

	score, latency, err := r.score(ctx, s, name)
	r.dec.ScoringLatency = latency
	r.p.deps.Observer.Observe(Event{Kind: EventScoring, Route: cr.Name, Strategy: name, Duration: latency})
	if err == nil {
		err = score.Validate()
	}
	if err != nil {
		cause := failureCause(err)
		if cause == CauseTimeout || cause == CauseRejected {
			r.p.deps.Observer.Observe(Event{Kind: EventScoringRejected, Route: cr.Name, Strategy: name, Reason: cause})
		}
		r.tier = r.p.opts.Difficulty.OnError
		r.dec.AddReason(decision.ReasonStrategyError(name, r.tier, cause))
		return
	}
	p := score.P
	r.dec.Score = &p
	r.dec.Variant = score.Variant
	r.dec.ScoringCostUSD = float64(score.EmbeddingInputTokens) * r.p.scoringPrice / 1e6
	if name == StrategyMF && score.EmbeddingInputTokens > 0 {
		r.p.deps.Observer.Observe(Event{Kind: EventMFExternalEmbedding, Route: cr.Name, Strategy: name})
	}
	r.tier = cr.Threshold.Tier(p)
	r.dec.AddReason(decision.ReasonStrategy(name, p, threshold))
}

func (r *request) score(
	ctx context.Context, s difficulty.Strategy, name string,
) (difficulty.Score, time.Duration, error) {
	clk := r.p.deps.Clock
	sctx, stop := withClockTimeout(ctx, clk, r.p.opts.strategyTimeout(name), errScoreTimeout)
	defer stop()
	start := clk.Now()
	score, err := s.Score(sctx, r.req)
	latency := clk.Now().Sub(start)
	if err != nil && errors.Is(context.Cause(sctx), errScoreTimeout) {
		err = errors.Join(errScoreTimeout, err)
	}
	return score, latency, err
}

func failureCause(err error) string {
	var rej Rejection
	switch {
	case errors.Is(err, errScoreTimeout), errors.Is(err, context.DeadlineExceeded):
		return CauseTimeout
	case errors.As(err, &rej) && rej.Rejected():
		return CauseRejected
	case errors.Is(err, difficulty.ErrNoScoredText):
		return CauseNoScoredText
	}
	return CauseError
}

func withClockTimeout(
	parent context.Context, clk clock.Clock, d time.Duration, cause error,
) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := clk.After(d)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-timer:
			cancel(cause)
		case <-done:
		case <-ctx.Done():
		}
	})
	return ctx, func() {
		close(done)
		wg.Wait()
		cancel(nil)
	}
}
