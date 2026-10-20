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

// Name is the strategy name used in configuration and decisions.
const Name = "bert"

// MaxInputTokens is the training maximum length and the upper bound of
// bert.max_input_tokens, including <s> and </s>.
const MaxInputTokens = 512

// Graph input and output names (research/README.md, graph contract).
const (
	InputIDs      = "input_ids"
	AttentionMask = "attention_mask"
	OutputLogits  = "logits"
)

// numLogits is the size of the classifier head: p_strong is class 0.
const numLogits = 3

// Variant is the ONNX artifact a Strategy runs.
type Variant string

// Variants (bert.variant; "auto" is resolved by the caller from the
// artifacts present).
const (
	VariantFP32 Variant = "fp32"
	VariantINT8 Variant = "int8"
)

// Errors of the strategy.
var (
	// ErrInvalidConfig is returned for invalid constructor arguments.
	ErrInvalidConfig = errors.New("bert: invalid config")
	// ErrBadOutput is returned when the model output is not 3 finite logits.
	ErrBadOutput = errors.New("bert: unexpected model output")
)

// Tokenizer encodes text into XLM-R token ids; *hftok.Tokenizer implements it.
type Tokenizer interface {
	Encode(text string, addSpecial bool) ([]int, error)
}

// Runner runs the graph; *onnxrt.Pool implements it. Errors meaning the work
// was refused without running (onnxrt.ErrQueueFull) implement Rejected() bool
// and are returned wrapped, so the route stage records cause "rejected".
type Runner interface {
	Run(ctx context.Context, inputs ...onnxrt.Tensor) ([]onnxrt.Tensor, error)
}

// Strategy is the bert difficulty strategy. It is safe for concurrent use;
// concurrency is bounded by the Runner.
type Strategy struct {
	tok       Tokenizer
	run       Runner
	variant   Variant
	maxTokens int
	closer    func(context.Context) error
}

var _ difficulty.Strategy = (*Strategy)(nil)

// Option configures a Strategy.
type Option func(*Strategy)

// WithMaxInputTokens sets bert.max_input_tokens (1..512, default 512).
func WithMaxInputTokens(n int) Option { return func(s *Strategy) { s.maxTokens = n } }

// New returns a Strategy over a tokenizer and a runner of the given variant.
// The caller owns tok and run; Close does nothing for such a Strategy.
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

// Name implements difficulty.Strategy.
func (*Strategy) Name() string { return Name }

// Variant returns the artifact variant the strategy runs.
func (s *Strategy) Variant() Variant { return s.variant }

// QueueDepth returns the number of scoring calls waiting for a pool slot
// (triage_scoring_queue_depth), or 0 when the Runner reports no statistics.
func (s *Strategy) QueueDepth() int {
	if st, ok := s.run.(interface{ Stats() onnxrt.PoolStats }); ok {
		return st.Stats().Queued
	}
	return 0
}

// Score implements difficulty.Strategy: the scored text is tokenised with
// <s>/</s>, right-truncated to the maximum keeping the final </s>, run as a
// batch of one with an all-ones attention mask, and p_strong is
// softmax(logits)[0] computed in float64.
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

// PStrong returns softmax(logits)[0] in float64 (RouteLLM: 1 - sum(softmax[-2:])).
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

// Close releases the pool and session opened by Load, waiting for runs in
// flight until ctx ends. It does nothing for a Strategy built with New.
func (s *Strategy) Close(ctx context.Context) error {
	if s.closer == nil {
		return nil
	}
	return s.closer(ctx)
}
