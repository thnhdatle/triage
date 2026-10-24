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

type EmbedProvider interface {
	Embed(ctx context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error)
}

type UsageEmbedder struct {
	p EmbedProvider
}

var _ difficulty.Embedder = (*UsageEmbedder)(nil)

func NewUsageEmbedder(p EmbedProvider) (*UsageEmbedder, error) {
	if p == nil {
		return nil, errors.New("mf: nil embed provider")
	}
	return &UsageEmbedder{p: p}, nil
}

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

type meter struct{ n atomic.Int64 }

type meterKey struct{}

func withMeter(ctx context.Context) (context.Context, *meter) {
	m := &meter{}
	return context.WithValue(ctx, meterKey{}, m), m
}

func (m *meter) tokens() int { return int(m.n.Load()) }
