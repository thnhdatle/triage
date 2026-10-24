// SPDX-License-Identifier: Apache-2.0

package mf_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/app/embedcache"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

const tolP = 1e-5

func fixtureU(t *testing.T) []float64 {
	t.Helper()
	f, err := mf.ParseSafetensors(readFile(t, filepath.Join(mfDir, mf.WeightsFileName)))
	require.NoError(t, err)
	u, err := mf.ComputeU(f, mf.StrongRow, mf.WeakRow)
	require.NoError(t, err)
	return u
}

func TestComputeUGolden(t *testing.T) {
	t.Parallel()
	ref := loadReference(t)
	require.Equal(t, mf.StrongRow, ref.StrongRow)
	require.Equal(t, mf.WeakRow, ref.WeakRow)
	u := fixtureU(t)
	require.Len(t, u, mf.Dimensions)
	var maxDiff float64
	for i := range u {
		maxDiff = math.Max(maxDiff, math.Abs(u[i]-ref.U[i]))
	}
	t.Logf("max |du| = %.3g", maxDiff)
	assert.Less(t, maxDiff, 1e-9)
}

func TestScoreGolden(t *testing.T) {
	t.Parallel()
	ref := loadReference(t)
	ids, vecs := embeddings(t)
	require.Len(t, ref.Scores, len(ids))
	p := &tableProvider{vecs: vecs, usage: 7}
	ue, err := mf.NewUsageEmbedder(p)
	require.NoError(t, err)
	cache, err := embedcache.New(ue, 1024)
	require.NoError(t, err)
	s, err := mf.New(cache, fixtureU(t))
	require.NoError(t, err)
	assert.Equal(t, "mf", s.Name())

	var maxDP, maxTorch float64
	for i, id := range ids {
		require.Equal(t, id, ref.Scores[i].ID)
		got, err := s.Score(t.Context(), userReq(id))
		require.NoError(t, err)
		require.NoError(t, got.Validate())
		assert.Empty(t, got.Variant)
		assert.Equal(t, 7, got.EmbeddingInputTokens, "miss reports upstream usage")
		maxDP = math.Max(maxDP, math.Abs(got.P-ref.Scores[i].PStrong))
		maxTorch = math.Max(maxTorch, math.Abs(got.P-ref.Scores[i].PMFModel))
	}
	t.Logf("max |dp| vs float64 reference = %.3g, vs reference MFModel = %.3g", maxDP, maxTorch)
	assert.LessOrEqual(t, maxDP, tolP)
	assert.LessOrEqual(t, maxTorch, tolP)
	assert.InDelta(t, 0.04355773491008071, mustScore(t, s, ids[0]).P, tolP)

	hit := mustScore(t, s, ids[1])
	assert.Equal(t, 0, hit.EmbeddingInputTokens, "cache hit costs nothing")
	assert.InDelta(t, 0.5944606850097978, hit.P, tolP)
	reqs := p.requests()
	require.Len(t, reqs, len(ids), "one upstream call per distinct text")
	assert.Equal(t, mf.DefaultModel, reqs[0].Model)
	assert.Equal(t, mf.Dimensions, reqs[0].Dimensions)
}

func mustScore(t *testing.T, s *mf.Strategy, text string) difficulty.Score {
	t.Helper()
	got, err := s.Score(t.Context(), userReq(text))
	require.NoError(t, err)
	return got
}

type vecEmbedder struct {
	out   [][]float32
	err   error
	model string
	dims  int
	texts []string
}

func (e *vecEmbedder) Embed(_ context.Context, model string, dims int, texts []string) ([][]float32, error) {
	e.model, e.dims, e.texts = model, dims, texts
	return e.out, e.err
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	emb := &vecEmbedder{}
	tests := []struct {
		name string
		emb  difficulty.Embedder
		u    []float64
		opts []mf.Option
	}{
		{name: "nil embedder", u: []float64{1}},
		{name: "empty vector", emb: emb},
		{name: "nil option", emb: emb, u: []float64{1}, opts: []mf.Option{nil}},
		{name: "empty model", emb: emb, u: []float64{1}, opts: []mf.Option{mf.WithModel("")}},
		{name: "zero max tokens", emb: emb, u: []float64{1}, opts: []mf.Option{mf.WithMaxInputTokens(0)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := mf.New(tc.emb, tc.u, tc.opts...)
			require.ErrorIs(t, err, mf.ErrInvalidConfig)
		})
	}
}

func TestScoreOptionsAndTruncation(t *testing.T) {
	t.Parallel()
	emb := &vecEmbedder{out: [][]float32{{1, 2}}}
	u := []float64{0.5, -0.25}
	s, err := mf.New(emb, u, mf.WithModel("deploy-1"), mf.WithMaxInputTokens(3))
	require.NoError(t, err)
	u[0] = 100
	got, err := s.Score(t.Context(), userReq(strings.Repeat("a", 40)))
	require.NoError(t, err)
	assert.InDelta(t, 0.5, got.P, 1e-15, "u.e = 0.5 - 0.5 = 0")
	assert.Equal(t, 0, got.EmbeddingInputTokens, "embedder without UsageEmbedder reports nothing")
	assert.Equal(t, "deploy-1", emb.model)
	assert.Equal(t, 2, emb.dims)
	assert.Equal(t, []string{strings.Repeat("a", 12)}, emb.texts, "3 estimated tokens = 12 ASCII bytes")
}

func TestScoreErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	imageOnly := chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartImage}}},
	}}
	tests := []struct {
		name string
		ctx  context.Context //nolint:containedctx // test table input.
		req  chat.Request
		emb  *vecEmbedder
		want error
	}{
		{name: "canceled", ctx: canceled, req: userReq("x"), want: context.Canceled},
		{name: "no user message", req: chat.Request{}, want: difficulty.ErrNoScoredText},
		{name: "no text", req: imageOnly, want: mf.ErrEmptyText},
		{name: "embed error", req: userReq("x"), emb: &vecEmbedder{err: boom}, want: boom},
		{name: "no vectors", req: userReq("x"), emb: &vecEmbedder{}, want: mf.ErrBadEmbedding},
		{
			name: "two vectors", req: userReq("x"), emb: &vecEmbedder{out: [][]float32{{1, 1}, {1, 1}}},
			want: mf.ErrBadEmbedding,
		},
		{name: "wrong size", req: userReq("x"), emb: &vecEmbedder{out: [][]float32{{1}}}, want: mf.ErrBadEmbedding},
		{
			name: "nan", req: userReq("x"), emb: &vecEmbedder{out: [][]float32{{float32(math.NaN()), 1}}},
			want: mf.ErrBadEmbedding,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := tc.ctx
			if ctx == nil {
				ctx = t.Context()
			}
			emb := tc.emb
			if emb == nil {
				emb = &vecEmbedder{out: [][]float32{{1, 1}}}
			}
			s, err := mf.New(emb, []float64{1, 1})
			require.NoError(t, err)
			_, err = s.Score(ctx, tc.req)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestUsageEmbedder(t *testing.T) {
	t.Parallel()
	_, err := mf.NewUsageEmbedder(nil)
	require.Error(t, err)

	vecs := map[string][]float32{"hello": {1, 0}, "ü": {0, 1}}
	p := &tableProvider{vecs: vecs}
	ue, err := mf.NewUsageEmbedder(p)
	require.NoError(t, err)
	s, err := mf.New(ue, []float64{0, 0})
	require.NoError(t, err)
	got, err := s.Score(t.Context(), userReq("hello"))
	require.NoError(t, err)
	assert.Equal(t, chat.EstimateTokens("hello"), got.EmbeddingInputTokens, "no upstream usage: F3 estimate")

	out, err := ue.Embed(t.Context(), "m", 2, []string{"hello", "ü"})
	require.NoError(t, err, "works without a meter in the context")
	assert.Len(t, out, 2)

	p.err = errors.New("upstream 500")
	_, err = s.Score(t.Context(), userReq("hello"))
	require.ErrorIs(t, err, p.err)
}

func TestSigmoid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		x    float64
		want float64
	}{
		{name: "zero", x: 0, want: 0.5},
		{name: "positive", x: 2, want: 1 / (1 + math.Exp(-2))},
		{name: "negative", x: -2, want: 1 / (1 + math.Exp(2))},
		{name: "very negative stays finite", x: -1000, want: 0},
		{name: "very positive", x: 1000, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tc.want, mf.Sigmoid(tc.x), 1e-15)
		})
	}
}

func TestTruncateEstimate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		max  int
		want string
	}{
		{name: "fits", text: "abcd", max: 1, want: "abcd"},
		{name: "ascii cut", text: "abcdefgh", max: 1, want: "abcd"},
		{name: "multibyte not split", text: "aaaaé", max: 1, want: "aaaa"},
		{name: "cjk", text: "一丁丂七", max: 2, want: "一丁"},
		{name: "invalid byte counts as non-ascii", text: "\xff\xff\xff\xff", max: 1, want: "\xff\xff\xff"},
		{name: "zero budget", text: "a", max: 0, want: ""},
		{name: "negative budget", text: "a", max: -1, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, mf.TruncateEstimate(tc.text, tc.max))
		})
	}
}

func TestTruncateEstimateProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.String().Draw(t, "text")
		limit := rapid.IntRange(0, 50).Draw(t, "max")
		got := mf.TruncateEstimate(text, limit)
		if !strings.HasPrefix(text, got) {
			t.Fatalf("%q is not a prefix of %q", got, text)
		}
		if chat.EstimateTokens(got) > limit {
			t.Fatalf("estimate %d > %d", chat.EstimateTokens(got), limit)
		}
		if got == text {
			return
		}
		_, w := utf8.DecodeRuneInString(text[len(got):])
		if chat.EstimateTokens(text[:len(got)+w]) <= limit {
			t.Fatalf("prefix %q could be longer", got)
		}
	})
}

func BenchmarkScore(b *testing.B) {
	u := make([]float64, mf.Dimensions)
	e := make([]float32, mf.Dimensions)
	for i := range u {
		u[i], e[i] = float64(i%7)-3, float32(i%5)/10
	}
	s, err := mf.New(&vecEmbedder{out: [][]float32{e}}, u)
	require.NoError(b, err)
	req := userReq(strings.Repeat("How do I reverse a linked list? ", 20))
	for b.Loop() {
		if _, err := s.Score(b.Context(), req); err != nil {
			b.Fatal(err)
		}
	}
}
