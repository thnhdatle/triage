// SPDX-License-Identifier: Apache-2.0

package difficulty

import (
	"context"
	"errors"
	"fmt"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
)

// Strategy scores how hard a request is.
type Strategy interface {
	Name() string
	Score(ctx context.Context, req chat.Request) (Score, error)
}

// Embedder turns texts into vectors.
type Embedder interface {
	Embed(ctx context.Context, model string, dimensions int, texts []string) ([][]float32, error)
}

type Score struct {
	P                    float64
	Variant              string
	EmbeddingInputTokens int
}

func (s Score) Validate() error {
	if !(s.P >= 0 && s.P <= 1) {
		return fmt.Errorf("difficulty: score %v outside [0,1]", s.P)
	}
	return nil
}

type Threshold float64

func (t Threshold) Tier(p float64) model.Tier {
	if p >= float64(t) {
		return model.TierStrong
	}
	return model.TierWeak
}

var ErrNoScoredText = errors.New("difficulty: request has no user message to score")

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
