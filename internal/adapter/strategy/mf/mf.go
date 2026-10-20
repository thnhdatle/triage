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

// Name is the strategy name used in configuration and decisions.
const Name = "mf"

// DefaultModel is the embedding model the projection was trained on.
const DefaultModel = "text-embedding-3-small"

// MaxInputTokens bounds the scored text by its F3 estimate (the embedding
// model's input limit with margin).
const MaxInputTokens = 8000

// Errors of the strategy.
var (
	// ErrInvalidConfig is returned for invalid constructor arguments.
	ErrInvalidConfig = errors.New("mf: invalid config")
	// ErrEmptyText is returned when the last user message has no text: the
	// embedding API rejects empty input.
	ErrEmptyText = errors.New("mf: scored text is empty")
	// ErrBadEmbedding is returned when the embedder answers with the wrong
	// number of vectors or a vector of the wrong size.
	ErrBadEmbedding = errors.New("mf: unexpected embedding")
)

// Strategy is the mf difficulty strategy: p_strong = sigmoid(u . e). It is
// immutable and safe for concurrent use.
type Strategy struct {
	emb       difficulty.Embedder
	u         []float64
	model     string
	maxTokens int
}

var _ difficulty.Strategy = (*Strategy)(nil)

// Option configures a Strategy.
type Option func(*Strategy)

// WithModel replaces DefaultModel (for a compatible deployment name).
func WithModel(m string) Option { return func(s *Strategy) { s.model = m } }

// WithMaxInputTokens replaces MaxInputTokens.
func WithMaxInputTokens(n int) Option { return func(s *Strategy) { s.maxTokens = n } }

// New returns a Strategy over an embedder and the vector u (LoadVector).
// The embedder is normally embedcache over UsageEmbedder, so that
// Score.EmbeddingInputTokens reports upstream usage on misses and 0 on hits.
// u is copied.
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

// Name implements difficulty.Strategy.
func (*Strategy) Name() string { return Name }

// Score implements difficulty.Strategy. The scored text is truncated to the
// token estimate bound and embedded with the vector's dimension;
// EmbeddingInputTokens is the usage UsageEmbedder recorded for this call (0
// when the cache answered).
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

// Sigmoid is the logistic function, stable for large |x|.
func Sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

// TruncateEstimate returns the longest prefix of text, cut at a rune
// boundary, whose chat.EstimateTokens is at most maxTokens.
func TruncateEstimate(text string, maxTokens int) string {
	if chat.EstimateTokens(text) <= maxTokens {
		return text
	}
	// EstimateTokens = ceil((3*ascii + 4*nonASCII) / 12) grows with every
	// byte, so the prefix is found in one pass.
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
