// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package bert_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/domain/artifact"
	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

const (
	fixtureDir = "../../../../testdata/models/bert-tiny"
	corpusPath = "../../../../testdata/tokenizer/corpus.jsonl"
)

const (
	tolFP32 = 1e-5
	tolINT8 = 1e-3
)

type refLine struct {
	ID      string    `json:"id"`
	NTokens int       `json:"n_tokens"`
	PTorch  float64   `json:"p_torch"`
	PFP32   float64   `json:"p_fp32"`
	PINT8   float64   `json:"p_int8"`
	Logits  []float64 `json:"logits_fp32"`
	Text    string    `json:"-"`
}

func readJSONL[T any](t *testing.T, path string) []T {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var v T
		require.NoError(t, json.Unmarshal(sc.Bytes(), &v))
		out = append(out, v)
	}
	require.NoError(t, sc.Err())
	return out
}

func references(t *testing.T) []refLine {
	t.Helper()
	refs := readJSONL[refLine](t, filepath.Join(fixtureDir, "reference.jsonl"))
	corpus := readJSONL[struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}](t, corpusPath)
	require.Len(t, refs, len(corpus))
	for i := range refs {
		require.Equal(t, corpus[i].ID, refs[i].ID)
		refs[i].Text = corpus[i].Text
	}
	return refs
}

func tokenizerPath(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("TRIAGE_TOKENIZER_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		dir = filepath.Join(home, ".cache", "rayld", "triage", "tokenizers")
	}
	p := filepath.Join(dir, "bert", "tokenizer.json")
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		require.FailNowf(t, "tokenizer file missing", "%s not found: run `make bootstrap` (or set TRIAGE_TOKENIZER_DIR)", p)
	}
	return p
}

func openRuntime(t *testing.T) *onnxrt.Runtime {
	t.Helper()
	rt, err := onnxrt.Open()
	require.NoError(t, err, "ONNX Runtime >= %s is required: run make bootstrap or set %s",
		onnxrt.MinVersion, onnxrt.EnvLibraryPath)
	t.Cleanup(func() { assert.NoError(t, rt.Close()) })
	return rt
}

func load(t *testing.T, rt *onnxrt.Runtime, model string, v bert.Variant) *bert.Strategy {
	t.Helper()
	s, err := bert.Load(t.Context(), rt, bert.Config{
		TokenizerPath: tokenizerPath(t), ModelPath: model, Variant: v,
		IntraOpThreads: 1, MaxConcurrency: 4, QueueSize: 64,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, s.Close(context.Background())) })
	return s
}

func TestGolden(t *testing.T) {
	t.Parallel()
	rt := openRuntime(t)
	refs := references(t)
	tests := []struct {
		name    string
		model   string
		variant bert.Variant
		want    func(refLine) float64
		tol     float64
	}{
		{
			name: "fp32", model: filepath.Join(fixtureDir, bert.FP32FileName), variant: bert.VariantFP32,
			want: func(r refLine) float64 { return r.PFP32 }, tol: tolFP32,
		},
		{
			name: "fp32 vs torch", model: filepath.Join(fixtureDir, bert.FP32FileName), variant: bert.VariantFP32,
			want: func(r refLine) float64 { return r.PTorch }, tol: tolFP32,
		},
		{
			name: "int8", model: filepath.Join(fixtureDir, bert.INT8FileName), variant: bert.VariantINT8,
			want: func(r refLine) float64 { return r.PINT8 }, tol: tolINT8,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := load(t, rt, tc.model, tc.variant)
			var maxDiff float64
			var wg sync.WaitGroup
			var mu sync.Mutex
			sem := make(chan struct{}, 8)
			for _, r := range refs {
				sem <- struct{}{}
				wg.Go(func() {
					defer func() { <-sem }()
					got, err := s.Score(t.Context(), userReq(r.Text))
					if err != nil {
						t.Errorf("%s: %v", r.ID, err)
						return
					}
					assert.Equal(t, string(tc.variant), got.Variant)
					d := math.Abs(got.P - tc.want(r))
					assert.LessOrEqual(t, d, tc.tol, "%s: got %v want %v", r.ID, got.P, tc.want(r))
					mu.Lock()
					maxDiff = math.Max(maxDiff, d)
					mu.Unlock()
				})
			}
			wg.Wait()
			t.Logf("%s: max |dp| over %d prompts = %.3g", tc.name, len(refs), maxDiff)
		})
	}
}

func TestGoldenLogits(t *testing.T) {
	t.Parallel()
	rt := openRuntime(t)
	tok, err := hftok.Load(tokenizerPath(t))
	require.NoError(t, err)
	sess, err := rt.NewSessionFromFile(filepath.Join(fixtureDir, bert.FP32FileName), onnxrt.SessionConfig{
		IntraOpThreads: 1,
		InputNames:     []string{bert.InputIDs, bert.AttentionMask},
		OutputNames:    []string{bert.OutputLogits},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sess.Close()) })
	run := &recordingRunner{next: sess}
	s, err := bert.New(tok, run, bert.VariantFP32)
	require.NoError(t, err)
	long := 0
	for _, r := range references(t) {
		_, err := s.Score(t.Context(), userReq(r.Text))
		require.NoError(t, err, r.ID)
		assert.Equal(t, r.NTokens, run.n, r.ID)
		if r.NTokens == bert.MaxInputTokens {
			long++
		}
		require.Len(t, run.logits, 3)
		for i := range 3 {
			assert.InDelta(t, r.Logits[i], float64(run.logits[i]), 1e-4, "%s logit %d", r.ID, i)
		}
	}
	assert.Equal(t, 15, long, "fixture contract: 15 prompts hit 512 tokens")
}

type recordingRunner struct {
	next   bert.Runner
	n      int
	logits []float32
}

func (r *recordingRunner) Run(ctx context.Context, in ...onnxrt.Tensor) ([]onnxrt.Tensor, error) {
	r.n = int(in[0].Shape()[1])
	out, err := r.next.Run(ctx, in...)
	if err == nil {
		r.logits, _ = out[0].Float32Data()
	}
	return out, err
}

func TestQueueFullRejects(t *testing.T) {
	t.Parallel()
	rt := openRuntime(t)
	tok, err := hftok.Load(tokenizerPath(t))
	require.NoError(t, err)
	sess, err := rt.NewSessionFromFile(filepath.Join(fixtureDir, bert.INT8FileName), onnxrt.SessionConfig{
		InputNames:  []string{bert.InputIDs, bert.AttentionMask},
		OutputNames: []string{bert.OutputLogits},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sess.Close()) })
	pool, err := onnxrt.NewPool(sess, onnxrt.PoolConfig{MaxConcurrency: 1, QueueSize: 0})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, pool.Close(context.Background())) })
	s, err := bert.New(tok, pool, bert.VariantINT8)
	require.NoError(t, err)

	lease, err := pool.Acquire(t.Context())
	require.NoError(t, err)
	_, err = s.Score(t.Context(), userReq("hello"))
	require.ErrorIs(t, err, onnxrt.ErrQueueFull)
	var rej interface{ Rejected() bool }
	require.ErrorAs(t, err, &rej)
	assert.True(t, rej.Rejected())
	lease.Release()

	got, err := s.Score(t.Context(), userReq("hello"))
	require.NoError(t, err)
	require.NoError(t, got.Validate())
}

func TestScoreDeadline(t *testing.T) {
	t.Parallel()
	rt := openRuntime(t)
	s := load(t, rt, filepath.Join(fixtureDir, bert.INT8FileName), bert.VariantINT8)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.Score(ctx, userReq("hello"))
	require.ErrorIs(t, err, context.Canceled)
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	rt := openRuntime(t)
	tokPath := tokenizerPath(t)
	fp32 := filepath.Join(fixtureDir, bert.FP32FileName)
	int8Model := filepath.Join(fixtureDir, bert.INT8FileName)

	tampered := t.TempDir()
	graph, err := os.ReadFile(fp32)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tampered, bert.FP32FileName), graph, 0o600))
	weights, err := os.ReadFile(filepath.Join(fixtureDir, "model.safetensors"))
	require.NoError(t, err)
	weights[len(weights)-1] ^= 0xff
	require.NoError(t, os.WriteFile(filepath.Join(tampered, "model.safetensors"), weights, 0o600))
	junk := filepath.Join(t.TempDir(), "junk.onnx")
	require.NoError(t, os.WriteFile(junk, []byte{0xff, 0xff, 0xff}, 0o600))

	tests := []struct {
		name string
		rt   *onnxrt.Runtime
		cfg  bert.Config
		want error
	}{
		{name: "nil runtime", cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: fp32, Variant: bert.VariantFP32,
			MaxConcurrency: 1,
		}, want: bert.ErrInvalidConfig},
		{name: "no paths", rt: rt, cfg: bert.Config{Variant: bert.VariantFP32}, want: bert.ErrInvalidConfig},
		{name: "unknown variant", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: int8Model, Variant: "auto",
			MaxConcurrency: 1,
		}, want: bert.ErrInvalidConfig},
		{name: "fp32 file as int8", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: fp32,
			Variant: bert.VariantINT8, MaxConcurrency: 1,
		}, want: bert.ErrInvalidModel},
		{name: "int8 file as fp32", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: int8Model,
			Variant: bert.VariantFP32, MaxConcurrency: 1,
		}, want: bert.ErrInvalidModel},
		{
			name: "tampered weights", rt: rt, cfg: bert.Config{
				TokenizerPath: tokPath,
				ModelPath:     filepath.Join(tampered, bert.FP32FileName), Variant: bert.VariantFP32, MaxConcurrency: 1,
			},
			want: bert.ErrWeightsMismatch,
		},
		{name: "missing model", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: junk + ".missing",
			Variant: bert.VariantINT8, MaxConcurrency: 1,
		}, want: fs.ErrNotExist},
		{name: "not onnx", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: junk,
			Variant: bert.VariantINT8, MaxConcurrency: 1,
		}, want: bert.ErrInvalidModel},
		{name: "missing tokenizer", rt: rt, cfg: bert.Config{
			TokenizerPath: junk + ".json", ModelPath: int8Model,
			Variant: bert.VariantINT8, MaxConcurrency: 1,
		}, want: fs.ErrNotExist},
		{name: "bad pool", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: int8Model,
			Variant: bert.VariantINT8, MaxConcurrency: 0,
		}, want: onnxrt.ErrInvalidConfig},
		{name: "bad max tokens", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: int8Model,
			Variant: bert.VariantINT8, MaxConcurrency: 1, MaxInputTokens: 600,
		}, want: bert.ErrInvalidConfig},
		{name: "bad threads", rt: rt, cfg: bert.Config{
			TokenizerPath: tokPath, ModelPath: int8Model,
			Variant: bert.VariantINT8, MaxConcurrency: 1, IntraOpThreads: -1,
		}, want: onnxrt.ErrInvalidConfig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := bert.Load(t.Context(), tc.rt, tc.cfg)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestCloseTwice(t *testing.T) {
	t.Parallel()
	rt := openRuntime(t)
	s, err := bert.Load(t.Context(), rt, bert.Config{
		TokenizerPath: tokenizerPath(t), ModelPath: filepath.Join(fixtureDir, bert.INT8FileName),
		Variant: bert.VariantINT8, MaxConcurrency: 1,
	})
	require.NoError(t, err)
	require.NoError(t, s.Close(t.Context()))
	require.NoError(t, s.Close(t.Context()))
	_, err = s.Score(t.Context(), userReq("x"))
	require.ErrorIs(t, err, onnxrt.ErrPoolClosed)
}

func BenchmarkScore(b *testing.B) {
	rt, err := onnxrt.Open()
	require.NoError(b, err)
	defer func() { require.NoError(b, rt.Close()) }()
	tok := filepath.Join(os.Getenv("HOME"), ".cache", "rayld", "triage", "tokenizers", "bert", "tokenizer.json")
	if dir := os.Getenv("TRIAGE_TOKENIZER_DIR"); dir != "" {
		tok = filepath.Join(dir, "bert", "tokenizer.json")
	}
	req := userReq("Explain the difference between a mutex and a semaphore, with an example in Go.")
	for _, v := range []struct {
		name    string
		file    string
		variant bert.Variant
	}{
		{name: "fp32", file: bert.FP32FileName, variant: bert.VariantFP32},
		{name: "int8", file: bert.INT8FileName, variant: bert.VariantINT8},
	} {
		b.Run(v.name, func(b *testing.B) {
			s, err := bert.Load(b.Context(), rt, bert.Config{
				TokenizerPath: tok, ModelPath: filepath.Join(fixtureDir, v.file), Variant: v.variant,
				MaxConcurrency: 1, QueueSize: 1,
			})
			require.NoError(b, err)
			defer func() { require.NoError(b, s.Close(context.Background())) }()
			for b.Loop() {
				if _, err := s.Score(b.Context(), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestDerivedFP32Loads(t *testing.T) {
	t.Parallel()
	d, err := bert.NewFP32Deriver(readFixture(t, bert.FP32FileName))
	require.NoError(t, err)
	dir, w := entryDir(t)
	e := artifact.Entry{Files: []artifact.File{{Path: "model.safetensors", SHA256: sha(w), Size: int64(len(w))}}}
	got, err := d.Derive(t.Context(), e, dir)
	require.NoError(t, err)
	s := load(t, openRuntime(t), got.Path, bert.VariantFP32)
	_, err = s.Score(t.Context(), userReq("hello"))
	require.NoError(t, err)
}
