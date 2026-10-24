// SPDX-License-Identifier: Apache-2.0

package bert

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

const Name = "bert"

const MaxInputTokens = 512

const (
	InputIDs      = "input_ids"
	AttentionMask = "attention_mask"
	OutputLogits  = "logits"
)

const numLogits = 3

type Variant string

const (
	VariantFP32 Variant = "fp32"
	VariantINT8 Variant = "int8"
)

var (
	ErrInvalidConfig = errors.New("bert: invalid config")
	ErrBadOutput     = errors.New("bert: unexpected model output")
)

type Tokenizer interface {
	Encode(text string, addSpecial bool) ([]int, error)
}

type Runner interface {
	Run(ctx context.Context, inputs ...onnxrt.Tensor) ([]onnxrt.Tensor, error)
}

// Strategy scores prompt difficulty with a BERT classifier.
type Strategy struct {
	tok       Tokenizer
	run       Runner
	variant   Variant
	maxTokens int
	closer    func(context.Context) error
}

var _ difficulty.Strategy = (*Strategy)(nil)

type Option func(*Strategy)

func WithMaxInputTokens(n int) Option { return func(s *Strategy) { s.maxTokens = n } }

// New returns a Strategy.
func New(tok Tokenizer, run Runner, variant Variant, opts ...Option) (*Strategy, error) {
	s := &Strategy{tok: tok, run: run, variant: variant, maxTokens: MaxInputTokens}
	for _, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidConfig)
		}
		o(s)
	}
	switch {
	case tok == nil:
		return nil, fmt.Errorf("%w: nil tokenizer", ErrInvalidConfig)
	case run == nil:
		return nil, fmt.Errorf("%w: nil runner", ErrInvalidConfig)
	case variant != VariantFP32 && variant != VariantINT8:
		return nil, fmt.Errorf("%w: variant %q (want %q or %q)", ErrInvalidConfig, variant, VariantFP32, VariantINT8)
	case s.maxTokens < 2 || s.maxTokens > MaxInputTokens:
		return nil, fmt.Errorf("%w: max input tokens %d (want 2..%d)", ErrInvalidConfig, s.maxTokens, MaxInputTokens)
	}
	return s, nil
}

func (*Strategy) Name() string { return Name }

func (s *Strategy) Variant() Variant { return s.variant }

func (s *Strategy) QueueDepth() int {
	if st, ok := s.run.(interface{ Stats() onnxrt.PoolStats }); ok {
		return st.Stats().Queued
	}
	return 0
}

// Score returns the probability that the prompt needs the strong model.
func (s *Strategy) Score(ctx context.Context, req chat.Request) (difficulty.Score, error) {
	if err := ctx.Err(); err != nil {
		return difficulty.Score{}, fmt.Errorf("bert: %w", err)
	}
	text, err := difficulty.ScoredText(req)
	if err != nil {
		return difficulty.Score{}, fmt.Errorf("bert: %w", err)
	}
	ids, err := s.tok.Encode(text, true)
	if err != nil {
		return difficulty.Score{}, fmt.Errorf("bert: tokenize: %w", err)
	}
	ids = hftok.Truncate(ids, s.maxTokens)
	if len(ids) == 0 {
		return difficulty.Score{}, errors.New("bert: tokenizer produced no ids")
	}
	logits, err := s.logits(ctx, ids)
	if err != nil {
		return difficulty.Score{}, err
	}
	return difficulty.Score{P: PStrong(logits), Variant: string(s.variant)}, nil
}

func (s *Strategy) logits(ctx context.Context, ids []int) ([]float32, error) {
	n := len(ids)
	in := make([]int64, n)
	mask := make([]int64, n)
	for i, id := range ids {
		in[i] = int64(id)
		mask[i] = 1
	}
	shape := []int64{1, int64(n)}
	out, err := s.run.Run(ctx, onnxrt.Int64Tensor(InputIDs, shape, in), onnxrt.Int64Tensor(AttentionMask, shape, mask))
	if err != nil {
		return nil, fmt.Errorf("bert: run: %w", err)
	}
	for _, t := range out {
		if t.Name() != OutputLogits {
			continue
		}
		v, err := t.Float32Data()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadOutput, err)
		}
		if len(v) != numLogits {
			return nil, fmt.Errorf("%w: %d logits, want %d", ErrBadOutput, len(v), numLogits)
		}
		for _, x := range v {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return nil, fmt.Errorf("%w: non-finite logit %v", ErrBadOutput, v)
			}
		}
		return v, nil
	}
	return nil, fmt.Errorf("%w: no %q output", ErrBadOutput, OutputLogits)
}

// PStrong returns softmax(logits)[0] in float64.
func PStrong(logits []float32) float64 {
	m := math.Inf(-1)
	for _, x := range logits {
		m = math.Max(m, float64(x))
	}
	var sum float64
	for _, x := range logits {
		sum += math.Exp(float64(x) - m)
	}
	return math.Exp(float64(logits[0])-m) / sum
}

func (s *Strategy) Close(ctx context.Context) error {
	if s.closer == nil {
		return nil
	}
	return s.closer(ctx)
}
