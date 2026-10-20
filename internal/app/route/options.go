// SPDX-License-Identifier: Apache-2.0

package route

import (
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// SelectionMode picks the order of eligible targets inside a tier
// (selection.mode).
type SelectionMode string

// Selection modes.
const (
	// SelectPriority keeps pool order. It is the default.
	SelectPriority SelectionMode = "priority"
	// SelectRoundRobin rotates the start of the tier's list per route and tier.
	SelectRoundRobin SelectionMode = "round_robin"
	// SelectLeastInflight orders by fewest in-flight requests, ties by pool order.
	SelectLeastInflight SelectionMode = "least_inflight"
)

// Valid reports whether m is a known mode.
func (m SelectionMode) Valid() bool {
	return m == SelectPriority || m == SelectRoundRobin || m == SelectLeastInflight
}

// Default strategy timeouts.
const (
	// DefaultStrategyTimeout applies to random, length and any strategy name
	// without an explicit timeout.
	DefaultStrategyTimeout = 50 * time.Millisecond
	// DefaultBERTTimeout is difficulty.bert.timeout.
	DefaultBERTTimeout = 300 * time.Millisecond
	// DefaultMFTimeout is difficulty.mf.timeout.
	DefaultMFTimeout = time.Second
	// DefaultEmbedMaxAttempts bounds the attempts of an embeddings request.
	DefaultEmbedMaxAttempts = 3
	// DefaultEmbedAttemptTimeout bounds one embeddings attempt unless the
	// target overrides it.
	DefaultEmbedAttemptTimeout = 60 * time.Second
)

// PrivacyOptions is the global privacy section.
type PrivacyOptions struct {
	// ScanSystem includes system messages in the scan scope.
	ScanSystem bool
	// MaxScanBytes is the scan limit; <= 0 means privacy.DefaultMaxScanBytes.
	MaxScanBytes int
	// OnOversize is local_only (default) or reject.
	OnOversize privacy.OversizeAction
}

// DifficultyOptions is the global difficulty section.
type DifficultyOptions struct {
	// OnError is the tier used when a strategy fails; empty means strong.
	OnError model.Tier
	// Timeouts maps strategy names to their Score timeout. Missing names use
	// DefaultBERTTimeout for "bert", DefaultMFTimeout for "mf" and
	// DefaultStrategyTimeout otherwise.
	Timeouts map[string]time.Duration
	// MFFallback is the strategy (a key of Deps.Strategies) used instead of
	// mf when the request has findings, is oversize or could not be scanned.
	// Required when a route uses mf.
	MFFallback string
	// MFCostTarget names the target whose InputPerMTok prices
	// Score.EmbeddingInputTokens (scoring_cost_usd). Empty or unknown
	// means scoring cost 0.
	MFCostTarget string
}

// SelectionOptions is the global selection section.
type SelectionOptions struct {
	// Mode orders targets inside a tier; empty means priority.
	Mode SelectionMode
	// Tokens counts prompt tokens for the context-window filter and the
	// min_prompt_tokens rule; nil means chat.Estimator.
	Tokens chat.TokenCounter
}

// EmbeddingOptions bounds the /v1/embeddings fallback.
type EmbeddingOptions struct {
	// MaxAttempts bounds the attempts; each eligible target is tried at most
	// once. <= 0 means DefaultEmbedMaxAttempts.
	MaxAttempts int
	// AttemptTimeout bounds one attempt unless model.Target.AttemptTimeout is
	// set; <= 0 means DefaultEmbedAttemptTimeout.
	AttemptTimeout time.Duration
}

// Options are the global settings of the pipeline. They are immutable once
// passed to New.
type Options struct {
	Privacy    PrivacyOptions
	Difficulty DifficultyOptions
	Selection  SelectionOptions
	Embeddings EmbeddingOptions
}

func (o Options) strategyTimeout(name string) time.Duration {
	if d, ok := o.Difficulty.Timeouts[name]; ok && d > 0 {
		return d
	}
	switch name {
	case "bert":
		return DefaultBERTTimeout
	case StrategyMF:
		return DefaultMFTimeout
	}
	return DefaultStrategyTimeout
}
