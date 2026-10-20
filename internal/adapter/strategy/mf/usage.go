// SPDX-License-Identifier: Apache-2.0

package mf

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// EmbedProvider is the embeddings call of a provider adapter
// (*openaicompat.Client implements it); unlike difficulty.Embedder it reports
// usage.
type EmbedProvider interface {
	Embed(ctx context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error)
}

// UsageEmbedder adapts an EmbedProvider to difficulty.Embedder and records
// the input tokens of each upstream call on the Score in flight.
// Wire it under the cache: mf.New(embedcache.New(mf.NewUsageEmbedder(c), n),
// u). On a cache hit it is not called, so the Score reports 0 tokens (F9).
type UsageEmbedder struct {
	p EmbedProvider
}

var _ difficulty.Embedder = (*UsageEmbedder)(nil)

// NewUsageEmbedder returns a UsageEmbedder over p.
func NewUsageEmbedder(p EmbedProvider) (*UsageEmbedder, error) {
	if p == nil {
		return nil, errors.New("mf: nil embed provider")
	}
	return &UsageEmbedder{p: p}, nil
}

// Embed implements difficulty.Embedder. The input tokens are the upstream's
// reported usage; when the upstream reports none, the F3 estimate of the
// texts is used instead.
func (u *UsageEmbedder) Embed(ctx context.Context, model string, dimensions int, texts []string) ([][]float32, error) {
	resp, err := u.p.Embed(ctx, provider.EmbedRequest{Model: model, Input: texts, Dimensions: dimensions})
	if err != nil {
		return nil, fmt.Errorf("mf: embeddings: %w", err)
	}
	n := resp.Usage.PromptTokens
	if n <= 0 {
		n = 0
		for _, t := range texts {
			n += chat.EstimateTokens(t)
		}
	}
	if m, ok := ctx.Value(meterKey{}).(*meter); ok {
		m.n.Add(int64(n))
	}
	return resp.Vectors, nil
}

// meter accumulates the embedding input tokens of one Score call.
type meter struct{ n atomic.Int64 }

type meterKey struct{}

func withMeter(ctx context.Context) (context.Context, *meter) {
	m := &meter{}
	return context.WithValue(ctx, meterKey{}, m), m
}

func (m *meter) tokens() int { return int(m.n.Load()) }
