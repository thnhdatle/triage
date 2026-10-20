// SPDX-License-Identifier: Apache-2.0

package difficulty

import (
	"context"
	"errors"
	"fmt"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
)

// Strategy is a difficulty scorer.
type Strategy interface {
	Name() string
	// Score returns p_strong in [0,1]: estimated probability the weak model is not good enough.
	Score(ctx context.Context, req chat.Request) (Score, error)
}

// Embedder is implemented by the openaicompat adapter, wrapped by app/embedcache, used only by mf.
type Embedder interface {
	Embed(ctx context.Context, model string, dimensions int, texts []string) ([][]float32, error)
}

// Score is the result of a strategy.
type Score struct {
	// P is p_strong in [0,1].
	P float64
	// Variant names the artifact variant used, e.g. "int8" or "fp32"; empty for
	// strategies without artifacts.
	Variant string
	// EmbeddingInputTokens is the input tokens of an external embedding call
	// made while scoring (mf cache misses only), used for scoring cost (F9).
	EmbeddingInputTokens int
}

// Validate reports an error when P is outside [0,1] or NaN.
func (s Score) Validate() error {
	if !(s.P >= 0 && s.P <= 1) {
		return fmt.Errorf("difficulty: score %v outside [0,1]", s.P)
	}
	return nil
}

// Threshold is a route's decision threshold: p_strong >= threshold => strong.
type Threshold float64

// Tier returns the tier a score selects.
func (t Threshold) Tier(p float64) model.Tier {
	if p >= float64(t) {
		return model.TierStrong
	}
	return model.TierWeak
}

// ErrNoScoredText is returned when a request has no user message.
var ErrNoScoredText = errors.New("difficulty: request has no user message to score")

// ScoredText returns the text a learned strategy scores: the
// text parts of the last user message, concatenated in order, with original
// (unmasked) values. Parts are joined with "\n" when there is more than one.
func ScoredText(req chat.Request) (string, error) {
	i := req.LastUserIndex()
	if i < 0 {
		return "", ErrNoScoredText
	}
	var texts []string
	for _, p := range req.Messages[i].Content {
		if p.Type == chat.PartText {
			texts = append(texts, p.Text)
		}
	}
	switch len(texts) {
	case 0:
		return "", nil
	case 1:
		return texts[0], nil
	}
	n := len(texts) - 1
	for _, t := range texts {
		n += len(t)
	}
	b := make([]byte, 0, n)
	for k, t := range texts {
		if k > 0 {
			b = append(b, '\n')
		}
		b = append(b, t...)
	}
	return string(b), nil
}
