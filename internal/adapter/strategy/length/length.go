// SPDX-License-Identifier: Apache-2.0

package length

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

const Name = "length"

const (
	DefaultMidpointTokens = 2048
	DefaultSlope          = 0.004
)

// Strategy scores prompt difficulty from the prompt length.
type Strategy struct {
	midpoint float64
	slope    float64
	counter  chat.TokenCounter
}

var _ difficulty.Strategy = (*Strategy)(nil)

type Option func(*Strategy)

func WithCounter(c chat.TokenCounter) Option { return func(s *Strategy) { s.counter = c } }

// New returns a Strategy.
func New(midpointTokens int, slope float64, opts ...Option) (*Strategy, error) {
	if midpointTokens < 0 {
		return nil, fmt.Errorf("length: midpoint_tokens %d must be >= 0", midpointTokens)
	}
	if !(slope > 0) || math.IsInf(slope, 0) {
		return nil, fmt.Errorf("length: slope %v must be finite and > 0", slope)
	}
	s := &Strategy{midpoint: float64(midpointTokens), slope: slope, counter: chat.Estimator{}}
	for _, o := range opts {
		if o == nil {
			return nil, errors.New("length: nil option")
		}
		o(s)
	}
	if s.counter == nil {
		return nil, errors.New("length: nil token counter")
	}
	return s, nil
}

func (*Strategy) Name() string { return Name }

// Score returns the probability that the prompt needs the strong model.
func (s *Strategy) Score(ctx context.Context, req chat.Request) (difficulty.Score, error) {
	if err := ctx.Err(); err != nil {
		return difficulty.Score{}, fmt.Errorf("length: %w", err)
	}
	if _, err := difficulty.ScoredText(req); err != nil {
		return difficulty.Score{}, fmt.Errorf("length: %w", err)
	}
	n := float64(req.PromptTokens(s.counter))
	return difficulty.Score{P: sigmoid(s.slope * (n - s.midpoint))}, nil
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
