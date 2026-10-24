// SPDX-License-Identifier: Apache-2.0

package bert_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func userReq(text string) chat.Request {
	return chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: text}}},
	}}
}

type fakeTok struct {
	err     error
	empty   bool
	gotText string
	gotSpec bool
}

func (f *fakeTok) Encode(text string, addSpecial bool) ([]int, error) {
	f.gotText, f.gotSpec = text, addSpecial
	if f.err != nil {
		return nil, f.err
	}
	if f.empty {
		return nil, nil
	}
	ids := []int{0}
	for i := range len(text) {
		ids = append(ids, 10+i)
	}
	return append(ids, 2), nil
}

type fakeRunner struct {
	mu   sync.Mutex
	in   []onnxrt.Tensor
	out  []onnxrt.Tensor
	err  error
	runs int
}

func (f *fakeRunner) Run(_ context.Context, inputs ...onnxrt.Tensor) ([]onnxrt.Tensor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs++
	f.in = inputs
	return f.out, f.err
}

func logitsOut(v ...float32) []onnxrt.Tensor {
	return []onnxrt.Tensor{onnxrt.Float32Tensor(bert.OutputLogits, []int64{1, int64(len(v))}, v)}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	tok, run := &fakeTok{}, &fakeRunner{}
	tests := []struct {
		name    string
		tok     bert.Tokenizer
		run     bert.Runner
		variant bert.Variant
		opts    []bert.Option
	}{
		{name: "nil tokenizer", run: run, variant: bert.VariantFP32},
		{name: "nil runner", tok: tok, variant: bert.VariantFP32},
		{name: "unknown variant", tok: tok, run: run, variant: "auto"},
		{name: "nil option", tok: tok, run: run, variant: bert.VariantINT8, opts: []bert.Option{nil}},
		{
			name: "max tokens too small", tok: tok, run: run, variant: bert.VariantINT8,
			opts: []bert.Option{bert.WithMaxInputTokens(1)},
		},
		{
			name: "max tokens too large", tok: tok, run: run, variant: bert.VariantINT8,
			opts: []bert.Option{bert.WithMaxInputTokens(513)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := bert.New(tc.tok, tc.run, tc.variant, tc.opts...)
			require.ErrorIs(t, err, bert.ErrInvalidConfig)
		})
	}
}

func TestScoreBuildsInputsAndSoftmax(t *testing.T) {
	t.Parallel()
	tok := &fakeTok{}
	run := &fakeRunner{out: append(
		[]onnxrt.Tensor{onnxrt.Int64Tensor("other", []int64{1}, []int64{7})},
		logitsOut(-1.0415518283843994, -0.7435486316680908, 2.643397808074951)...)}
	s, err := bert.New(tok, run, bert.VariantINT8)
	require.NoError(t, err)
	assert.Equal(t, "bert", s.Name())
	assert.Equal(t, bert.VariantINT8, s.Variant())

	req := chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: "old"}}},
		{Role: chat.RoleAssistant, Content: []chat.Part{{Type: chat.PartText, Text: "answer"}}},
		{Role: chat.RoleUser, Content: []chat.Part{
			{Type: chat.PartText, Text: "ab"}, {Type: chat.PartImage}, {Type: chat.PartText, Text: "c"},
		}},
	}}
	got, err := s.Score(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, "ab\nc", tok.gotText, "last user message, text parts joined")
	assert.True(t, tok.gotSpec, "special tokens added")
	assert.InDelta(t, 0.023702140779158314, got.P, 1e-9)
	assert.Equal(t, "int8", got.Variant)
	require.NoError(t, got.Validate())

	require.Len(t, run.in, 2)
	assert.Equal(t, bert.InputIDs, run.in[0].Name())
	assert.Equal(t, []int64{1, 6}, run.in[0].Shape())
	ids, err := run.in[0].Int64Data()
	require.NoError(t, err)
	assert.Equal(t, []int64{0, 10, 11, 12, 13, 2}, ids)
	assert.Equal(t, bert.AttentionMask, run.in[1].Name())
	mask, err := run.in[1].Int64Data()
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 1, 1, 1, 1, 1}, mask)
}

func TestScoreTruncation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		textLen int
		max     int
		wantLen int
	}{
		{name: "short kept", textLen: 3, max: 512, wantLen: 5},
		{name: "exactly max", textLen: 510, max: 512, wantLen: 512},
		{name: "one over max", textLen: 511, max: 512, wantLen: 512},
		{name: "far over max", textLen: 5000, max: 512, wantLen: 512},
		{name: "custom max", textLen: 100, max: 16, wantLen: 16},
		{name: "minimum max", textLen: 100, max: 2, wantLen: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := &fakeRunner{out: logitsOut(0, 0, 0)}
			s, err := bert.New(&fakeTok{}, run, bert.VariantFP32, bert.WithMaxInputTokens(tc.max))
			require.NoError(t, err)
			text := make([]byte, tc.textLen)
			for i := range text {
				text[i] = 'a'
			}
			got, err := s.Score(t.Context(), userReq(string(text)))
			require.NoError(t, err)
			assert.InDelta(t, 1.0/3, got.P, 1e-15)
			ids, err := run.in[0].Int64Data()
			require.NoError(t, err)
			require.Len(t, ids, tc.wantLen)
			assert.Equal(t, int64(0), ids[0], "<s> first")
			assert.Equal(t, int64(2), ids[len(ids)-1], "</s> kept last")
		})
	}
}

type rejectedErr struct{}

func (rejectedErr) Error() string  { return "rejected" }
func (rejectedErr) Rejected() bool { return true }

func TestScoreErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context //nolint:containedctx // test table input.
		req  chat.Request
		tok  *fakeTok
		run  *fakeRunner
		want error
	}{
		{name: "canceled", ctx: canceled, req: userReq("x"), want: context.Canceled},
		{name: "no user message", req: chat.Request{}, want: difficulty.ErrNoScoredText},
		{name: "tokenizer error", req: userReq("x"), tok: &fakeTok{err: boom}, want: boom},
		{name: "no ids", req: userReq("x"), tok: &fakeTok{empty: true}},
		{name: "runner error", req: userReq("x"), run: &fakeRunner{err: boom}, want: boom},
		{name: "queue full", req: userReq("x"), run: &fakeRunner{err: onnxrt.ErrQueueFull}, want: onnxrt.ErrQueueFull},
		{
			name: "missing logits", req: userReq("x"),
			run:  &fakeRunner{out: []onnxrt.Tensor{onnxrt.Float32Tensor("x", []int64{1}, []float32{1})}},
			want: bert.ErrBadOutput,
		},
		{
			name: "int logits", req: userReq("x"),
			run:  &fakeRunner{out: []onnxrt.Tensor{onnxrt.Int64Tensor(bert.OutputLogits, []int64{3}, []int64{1, 2, 3})}},
			want: bert.ErrBadOutput,
		},
		{name: "two logits", req: userReq("x"), run: &fakeRunner{out: logitsOut(1, 2)}, want: bert.ErrBadOutput},
		{
			name: "nan logit", req: userReq("x"), run: &fakeRunner{out: logitsOut(1, float32(math.NaN()), 2)},
			want: bert.ErrBadOutput,
		},
		{
			name: "inf logit", req: userReq("x"), run: &fakeRunner{out: logitsOut(float32(math.Inf(1)), 1, 2)},
			want: bert.ErrBadOutput,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := tc.ctx
			if ctx == nil {
				ctx = t.Context()
			}
			tok, run := tc.tok, tc.run
			if tok == nil {
				tok = &fakeTok{}
			}
			if run == nil {
				run = &fakeRunner{out: logitsOut(0, 0, 0)}
			}
			s, err := bert.New(tok, run, bert.VariantFP32)
			require.NoError(t, err)
			_, err = s.Score(ctx, tc.req)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestScoreRejectionIsVisible(t *testing.T) {
	t.Parallel()
	s, err := bert.New(&fakeTok{}, &fakeRunner{err: rejectedErr{}}, bert.VariantFP32)
	require.NoError(t, err)
	_, err = s.Score(t.Context(), userReq("x"))
	var rej interface{ Rejected() bool }
	require.ErrorAs(t, err, &rej)
	assert.True(t, rej.Rejected())
	require.NoError(t, s.Close(t.Context()), "Close of a New strategy does nothing")
}

func TestPStrong(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		logits []float32
		want   float64
	}{
		{name: "uniform", logits: []float32{3, 3, 3}, want: 1.0 / 3},
		{name: "large values stay finite", logits: []float32{1000, 1000, -1000}, want: 0.5},
		{name: "en-002", logits: []float32{-1.7330015897750854, -0.8535478115081787, 3.8}, want: pRef(
			-1.7330015897750854, -0.8535478115081787, 3.8)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tc.want, bert.PStrong(tc.logits), 1e-15)
		})
	}
}

func pRef(a, b, c float32) float64 {
	ea, eb, ec := math.Exp(float64(a)), math.Exp(float64(b)), math.Exp(float64(c))
	return ea / (ea + eb + ec)
}

type statsRunner struct {
	fakeRunner
	queued int
}

func (s *statsRunner) Stats() onnxrt.PoolStats { return onnxrt.PoolStats{Queued: s.queued} }

func TestStrategy_QueueDepth(t *testing.T) {
	t.Parallel()
	s, err := bert.New(&fakeTok{}, &statsRunner{queued: 3}, bert.VariantINT8)
	require.NoError(t, err)
	assert.Equal(t, 3, s.QueueDepth())
	plain, err := bert.New(&fakeTok{}, &fakeRunner{}, bert.VariantINT8)
	require.NoError(t, err)
	assert.Equal(t, 0, plain.QueueDepth())
}
