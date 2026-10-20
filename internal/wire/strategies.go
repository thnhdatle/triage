// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/adapter/strategy/length"
	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/adapter/strategy/random"
	"github.com/thnhdatle/triage/internal/app/embedcache"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/platform/cpu"
	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

// observedStrategy records the score histogram and the scoring queue depth
// around a strategy. Errors pass through unchanged, so the route
// stage still sees Rejected() and timeouts.
type observedStrategy struct {
	next difficulty.Strategy
	tel  *telemetry.Telemetry
}

// Name implements difficulty.Strategy.
func (s observedStrategy) Name() string { return s.next.Name() }

// Score implements difficulty.Strategy.
func (s observedStrategy) Score(ctx context.Context, req chat.Request) (difficulty.Score, error) {
	if q, ok := s.next.(interface{ QueueDepth() int }); ok {
		s.tel.SetScoringQueueDepth(s.next.Name(), q.QueueDepth())
	}
	sc, err := s.next.Score(ctx, req)
	if err == nil && sc.Validate() == nil {
		s.tel.ObserveScore(s.next.Name(), sc.P)
	}
	return sc, err //nolint:wrapcheck // decorator: the route stage inspects the error
}

// tokenCounter adapts hftok to chat.TokenCounter (selection.tokenizer, F3).
type tokenCounter struct{ tok *hftok.Tokenizer }

// CountTokens implements chat.TokenCounter.
func (c tokenCounter) CountTokens(text string) int { return c.tok.Count(text) }

// ortHolder opens the process-wide ONNX Runtime once, on first need,
// and closes it at shutdown.
type ortHolder struct {
	path string // onnx.library_path at startup
	log  *slog.Logger

	mu sync.Mutex // guards rt
	rt *onnxrt.Runtime
}

func (h *ortHolder) get() (*onnxrt.Runtime, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rt != nil {
		return h.rt, nil
	}
	rt, err := onnxrt.Open(onnxrt.WithLibraryPath(h.path))
	if err != nil {
		return nil, fmt.Errorf("onnx runtime: %w", err)
	}
	h.log.Info(fmt.Sprintf("onnx runtime %s loaded from %s", rt.Version(), rt.LibraryPath()), "component", "onnxrt")
	h.rt = rt
	return rt, nil
}

func (h *ortHolder) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rt == nil {
		return nil
	}
	err := h.rt.Close()
	h.rt = nil
	if err != nil {
		return fmt.Errorf("onnx runtime close: %w", err)
	}
	return nil
}

// bertConfig sizes the bert scoring pool from difficulty.bert.
func bertConfig(files bertFiles, c config.BERT) bert.Config {
	conc := c.MaxConcurrency
	if conc <= 0 {
		conc = max(1, cpu.PhysicalCores()/max(1, c.IntraOpThreads))
	}
	return bert.Config{
		TokenizerPath: files.Tokenizer, ModelPath: files.Model, Variant: files.Variant,
		MaxInputTokens: c.MaxInputTokens, IntraOpThreads: c.IntraOpThreads, MaxConcurrency: conc,
		QueueSize: c.QueueSize,
	}
}

func bertKey(c bert.Config, artifactKey string) string {
	return fmt.Sprintf("bert:%s|%s|%d|%d|%d|%d", artifactKey, c.Variant, c.MaxInputTokens, c.IntraOpThreads,
		c.MaxConcurrency, c.QueueSize)
}

// simpleStrategy builds the artifact-free strategies.
func simpleStrategy(name string, cfg *config.Config, counter chat.TokenCounter) (difficulty.Strategy, error) {
	switch name {
	case strategyRandom:
		s, err := random.New()
		if err != nil {
			return nil, fmt.Errorf("strategy random: %w", err)
		}
		return s, nil
	case strategyLength:
		var opts []length.Option
		if counter != nil {
			opts = append(opts, length.WithCounter(counter))
		}
		s, err := length.New(cfg.Difficulty.Length.MidpointTokens, cfg.Difficulty.Length.Slope, opts...)
		if err != nil {
			return nil, fmt.Errorf("strategy length: %w", err)
		}
		return s, nil
	}
	return nil, fmt.Errorf("strategy %q needs artifacts", name)
}

// newMF builds mf over an embeddings provider: UsageEmbedder (usage for F9)
// under the LRU cache.
func newMF(p mf.EmbedProvider, vectorPath string, cacheSize int) (*mf.Strategy, error) {
	u, err := mf.LoadVector(vectorPath)
	if err != nil {
		return nil, fmt.Errorf("strategy mf: %w", err)
	}
	ue, err := mf.NewUsageEmbedder(p)
	if err != nil {
		return nil, fmt.Errorf("strategy mf: %w", err)
	}
	var emb difficulty.Embedder = ue
	if cacheSize > 0 {
		c, err := embedcache.New(ue, cacheSize)
		if err != nil {
			return nil, fmt.Errorf("strategy mf: %w", err)
		}
		emb = c
	}
	s, err := mf.New(emb, u)
	if err != nil {
		return nil, fmt.Errorf("strategy mf: %w", err)
	}
	return s, nil
}
