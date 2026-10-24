// SPDX-License-Identifier: Apache-2.0

package mf

import (
	"context"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

const Name = "mf"

const DefaultModel = "text-embedding-3-small"

const MaxInputTokens = 8000

var (
	ErrInvalidConfig = errors.New("mf: invalid config")
	ErrEmptyText     = errors.New("mf: scored text is empty")
	ErrBadEmbedding  = errors.New("mf: unexpected embedding")
)

// Strategy scores prompt difficulty from an embedding of the text.
type Strategy struct {
	emb       difficulty.Embedder
	u         []float64
	model     string
	maxTokens int
}

var _ difficulty.Strategy = (*Strategy)(nil)

type Option func(*Strategy)

func WithModel(m string) Option { return func(s *Strategy) { s.model = m } }

func WithMaxInputTokens(n int) Option { return func(s *Strategy) { s.maxTokens = n } }

// New returns a Strategy.
func New(emb difficulty.Embedder, u []float64, opts ...Option) (*Strategy, error) {
	s := &Strategy{emb: emb, u: append([]float64(nil), u...), model: DefaultModel, maxTokens: MaxInputTokens}
	for _, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidConfig)
		}
		o(s)
	}
	switch {
	case emb == nil:
		return nil, fmt.Errorf("%w: nil embedder", ErrInvalidConfig)
	case len(u) == 0:
		return nil, fmt.Errorf("%w: empty vector", ErrInvalidConfig)
	case s.model == "":
		return nil, fmt.Errorf("%w: empty embedding model", ErrInvalidConfig)
	case s.maxTokens < 1:
		return nil, fmt.Errorf("%w: max input tokens %d", ErrInvalidConfig, s.maxTokens)
	}
	return s, nil
}

func (*Strategy) Name() string { return Name }

// Score returns the probability that the prompt needs the strong model.
func (s *Strategy) Score(ctx context.Context, req chat.Request) (difficulty.Score, error) {
	if err := ctx.Err(); err != nil {
		return difficulty.Score{}, fmt.Errorf("mf: %w", err)
	}
	text, err := difficulty.ScoredText(req)
	if err != nil {
		return difficulty.Score{}, fmt.Errorf("mf: %w", err)
	}
	if text == "" {
		return difficulty.Score{}, ErrEmptyText
	}
	text = TruncateEstimate(text, s.maxTokens)
	ctx, m := withMeter(ctx)
	vecs, err := s.emb.Embed(ctx, s.model, len(s.u), []string{text})
	if err != nil {
		return difficulty.Score{}, fmt.Errorf("mf: embed: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != len(s.u) {
		n := 0
		if len(vecs) > 0 {
			n = len(vecs[0])
		}
		return difficulty.Score{}, fmt.Errorf("%w: %d vectors, first of size %d, want 1 of size %d",
			ErrBadEmbedding, len(vecs), n, len(s.u))
	}
	var dot float64
	for i, x := range vecs[0] {
		dot += s.u[i] * float64(x)
	}
	if math.IsNaN(dot) || math.IsInf(dot, 0) {
		return difficulty.Score{}, fmt.Errorf("%w: non-finite embedding", ErrBadEmbedding)
	}
	return difficulty.Score{P: Sigmoid(dot), EmbeddingInputTokens: m.tokens()}, nil
}

func Sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

func TruncateEstimate(text string, maxTokens int) string {
	if chat.EstimateTokens(text) <= maxTokens {
		return text
	}
	budget := 12 * max(maxTokens, 0)
	cost := 0
	for i := 0; i < len(text); {
		c, w := 3, 1
		if text[i] >= utf8.RuneSelf {
			_, w = utf8.DecodeRuneInString(text[i:])
			c = 4 * w
		}
		if cost+c > budget {
			return text[:i]
		}
		cost += c
		i += w
	}
	return text
}
