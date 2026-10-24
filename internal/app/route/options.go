// SPDX-License-Identifier: Apache-2.0

package route

import (
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

type SelectionMode string

const (
	SelectPriority      SelectionMode = "priority"
	SelectRoundRobin    SelectionMode = "round_robin"
	SelectLeastInflight SelectionMode = "least_inflight"
)

func (m SelectionMode) Valid() bool {
	return m == SelectPriority || m == SelectRoundRobin || m == SelectLeastInflight
}

const (
	DefaultStrategyTimeout     = 50 * time.Millisecond
	DefaultBERTTimeout         = 300 * time.Millisecond
	DefaultMFTimeout           = time.Second
	DefaultEmbedMaxAttempts    = 3
	DefaultEmbedAttemptTimeout = 60 * time.Second
)

type PrivacyOptions struct {
	ScanSystem   bool
	MaxScanBytes int
	OnOversize   privacy.OversizeAction
}

type DifficultyOptions struct {
	OnError      model.Tier
	Timeouts     map[string]time.Duration
	MFFallback   string
	MFCostTarget string
}

type SelectionOptions struct {
	Mode   SelectionMode
	Tokens chat.TokenCounter
}

type EmbeddingOptions struct {
	MaxAttempts    int
	AttemptTimeout time.Duration
}

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
