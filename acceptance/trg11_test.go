// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

const (
	trg11ID             = "TRG-11"
	trg11PythonTimeout  = 45 * time.Minute
	trg11ORTPrompts     = 200 // prompts of the corpus used for the bert comparisons
	trg11StrongRatioLow = 0.3
	trg11StrongRatioMid = 0.5
	trg11ReportOnly     = "report_only"
)

// trg11Artifacts is the artifact set the bert and mf comparisons run on.
type trg11Artifacts struct {
	Kind          string `json:"kind"` // "real" or "fixture"
	BertRevision  string `json:"bert_revision,omitempty"`
	MFRevision    string `json:"mf_revision,omitempty"`
	Embeddings    string `json:"embeddings"`
	checkpointDir string // config.json + model.safetensors (+ graph.onnx for fixtures)
	fp32          string // FP32 graph.onnx, next to model.safetensors
	graphSource   string // Python --model for FP32 (the weight-free graph)
	int8          string
	mfDir         string
}

// trg11Corpus is one line of testdata/tokenizer/corpus.jsonl.
type trg11Corpus struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Lang string `json:"lang"`
}

func trg11ReadJSONL[T any](tb testing.TB, path string) []T {
	tb.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: test-controlled path
	require.NoError(tb, err)
	defer func() { require.NoError(tb, f.Close()) }()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<26)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var v T
		require.NoError(tb, json.Unmarshal(sc.Bytes(), &v))
		out = append(out, v)
	}
	require.NoError(tb, sc.Err())
	return out
}

// trg11Python runs `uv run --project research --frozen python -m module args...` from the
// repository root.
func trg11Python(tb testing.TB, module string, args ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(tb.Context(), trg11PythonTimeout)
	defer cancel()
	argv := append([]string{"run", "--project", "research", "--frozen", "python", "-m", module}, args...)
	cmd := exec.CommandContext(ctx, "uv", argv...) //nolint:gosec // G204: fixed argv, no shell
	cmd.Dir = ".."
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	tb.Logf("%s finished in %s", module, time.Since(start).Round(time.Second))
	if err != nil {
		tail := out.String()
		if len(tail) > 4000 {
			tail = tail[len(tail)-4000:]
		}
		tb.Fatalf("python %s: %v\n%s", module, err, tail)
	}
}

// trg11Select picks the real artifacts on the Mac when they were pulled and the fixtures
// otherwise (always on CI). TRIAGE_TRG11_ARTIFACTS=fixture forces the fixtures.
func trg11Select(tb testing.TB, env string) trg11Artifacts {
	tb.Helper()
	fixtures := trg11Artifacts{
		Kind: "fixture", Embeddings: "synthetic (testdata/models/embeddings)",
		checkpointDir: "../testdata/models/bert-tiny",
		fp32:          "../testdata/models/bert-tiny/graph.onnx",
		graphSource:   "../testdata/models/bert-tiny/graph.onnx",
		int8:          "../testdata/models/bert-tiny/model.int8.onnx",
		mfDir:         "../testdata/models/mf-tiny",
	}
	paths := []*string{&fixtures.checkpointDir, &fixtures.fp32, &fixtures.graphSource, &fixtures.int8, &fixtures.mfDir}
	for _, p := range paths {
		abs, err := filepath.Abs(*p)
		require.NoError(tb, err)
		*p = abs
	}
	if env != EnvMac || os.Getenv("TRIAGE_TRG11_ARTIFACTS") == "fixture" {
		return fixtures
	}
	home, err := os.UserHomeDir()
	require.NoError(tb, err)
	cache := filepath.Join(home, ".cache", "rayld", "triage", "models")
	bdirs, _ := filepath.Glob(filepath.Join(cache, "bert", "*", "model.int8.onnx"))
	mdirs, _ := filepath.Glob(filepath.Join(cache, "mf", "*", "model.safetensors"))
	if len(bdirs) != 1 || len(mdirs) != 1 {
		return fixtures
	}
	bdir, mdir := filepath.Dir(bdirs[0]), filepath.Dir(mdirs[0])
	for _, f := range []string{"config.json", "model.safetensors"} {
		if _, err := os.Stat(filepath.Join(bdir, f)); err != nil {
			return fixtures
		}
	}
	// The derived FP32 graph is the committed weight-free graph placed next to the weights (ORT
	// refuses external data outside the graph directory, so no symlinks), as `models pull` does.
	place := bdir
	graph, err := os.ReadFile("../models/bert/graph.onnx")
	require.NoError(tb, err)
	if cur, err := os.ReadFile(filepath.Join(place, "graph.onnx")); err != nil || !bytes.Equal(cur, graph) {
		require.NoError(tb, os.WriteFile(filepath.Join(place, "graph.onnx"), graph, 0o600))
	}
	return trg11Artifacts{
		Kind: "real", BertRevision: filepath.Base(bdir), MFRevision: filepath.Base(mdir),
		Embeddings:    "synthetic (testdata/models/embeddings; no OpenAI key)",
		checkpointDir: place, fp32: filepath.Join(place, "graph.onnx"),
		graphSource: filepath.Join(place, "graph.onnx"),
		int8:        bdirs[0], mfDir: mdir,
	}
}

func trg11Req(text string) chat.Request {
	return chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: text}}},
	}}
}

// trg11GoBert scores every prompt with the Go bert strategy.
func trg11GoBert(
	ctx context.Context, tb testing.TB, rt *onnxrt.Runtime, tokenizer, model string, v bert.Variant, prompts []trg11Corpus,
) map[string]float64 {
	tb.Helper()
	s, err := bert.Load(ctx, rt, bert.Config{
		TokenizerPath: tokenizer, ModelPath: model, Variant: v,
		IntraOpThreads: 1, MaxConcurrency: 1, QueueSize: 1,
	})
	require.NoError(tb, err)
	defer func() { require.NoError(tb, s.Close(context.WithoutCancel(ctx))) }()
	out := make(map[string]float64, len(prompts))
	for _, p := range prompts {
		sc, err := s.Score(ctx, trg11Req(p.Text))
		require.NoError(tb, err, p.ID)
		out[p.ID] = sc.P
	}
	return out
}

type trg11Scored struct {
	ID      string  `json:"id"`
	PStrong float64 `json:"p_strong"`
}

func trg11ByID(rows []trg11Scored) map[string]float64 {
	m := make(map[string]float64, len(rows))
	for _, r := range rows {
		m[r.ID] = r.PStrong
	}
	return m
}

// trg11MaxDiff returns the largest |a-b| over the ids of prompts and the id where it occurs.
func trg11MaxDiff(tb testing.TB, prompts []trg11Corpus, a, b map[string]float64) (float64, string) {
	tb.Helper()
	worst, at := 0.0, ""
	for _, p := range prompts {
		x, okA := a[p.ID]
		y, okB := b[p.ID]
		require.True(tb, okA && okB, "missing score for %s", p.ID)
		if d := math.Abs(x - y); d > worst {
			worst, at = d, p.ID
		}
	}
	return worst, at
}

// trg11Agreement is the fraction of prompts on which a and b route alike at the threshold that
// sends the top `ratio` of the reference scores to the strong model.
func trg11Agreement(prompts []trg11Corpus, ref, got map[string]float64, ratio float64) float64 {
	scores := make([]float64, 0, len(prompts))
	for _, p := range prompts {
		scores = append(scores, ref[p.ID])
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
	k := int(math.Round(ratio * float64(len(scores))))
	if k < 1 {
		k = 1
	}
	thr := scores[k-1]
	same := 0
	for _, p := range prompts {
		if (ref[p.ID] >= thr) == (got[p.ID] >= thr) {
			same++
		}
	}
	return float64(same) / float64(len(prompts))
}

// trg11Tokenizer compares hftok with HF tokenizers (live Python) and returns identical, total.
func trg11Tokenizer(tb testing.TB, corpus []trg11Corpus, dir string) (same, total int, mismatches []string) {
	tb.Helper()
	work := tb.TempDir()
	data, err := os.ReadFile("../testdata/tokenizer/corpus.jsonl")
	require.NoError(tb, err)
	require.NoError(tb, os.WriteFile(filepath.Join(work, "corpus.jsonl"), data, 0o600))
	trg11Python(tb, "triage_research.reference_tokenize", "reference", "--out-dir", work, "--tokenizer-dir", dir)
	type idsLine struct {
		ID  string `json:"id"`
		IDs []int  `json:"ids"`
	}
	for _, name := range []string{"bert", "ner"} {
		tok, err := hftok.Load(filepath.Join(dir, name, "tokenizer.json"))
		require.NoError(tb, err)
		byID := map[string]trg11Corpus{}
		for _, c := range corpus {
			byID[c.ID] = c
		}
		for _, ref := range trg11ReadJSONL[idsLine](tb, filepath.Join(work, "reference_"+name+".jsonl")) {
			total++
			got, err := tok.Encode(byID[ref.ID].Text, true)
			if err == nil && fmt.Sprint(got) == fmt.Sprint(ref.IDs) {
				same++
			} else {
				mismatches = append(mismatches, name+":"+ref.ID)
			}
		}
	}
	return same, total, mismatches
}

// trg11Embedder answers the mf strategy from recorded vectors keyed by the text "e<i>".
type trg11Embedder struct{ vecs [][]float32 }

func (e trg11Embedder) Embed(_ context.Context, _ string, _ int, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		n, err := strconv.Atoi(strings.TrimPrefix(t, "e"))
		if err != nil || n < 0 || n >= len(e.vecs) {
			return nil, fmt.Errorf("unknown embedding key %q", t)
		}
		out[i] = e.vecs[n]
	}
	return out, nil
}

// trg11MF scores the recorded embeddings with the Go mf strategy and returns the largest
// |dp| against RouteLLM MFModel (reference_mf p_mfmodel) and against the closed form.
func trg11MF(tb testing.TB, dir string) (maxMFModel, maxClosed float64, n int) {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "model.safetensors")) //nolint:gosec // G304: test-controlled path
	require.NoError(tb, err)
	f, err := mf.ParseSafetensors(raw)
	require.NoError(tb, err)
	u, err := mf.ComputeU(f, mf.StrongRow, mf.WeakRow)
	require.NoError(tb, err)

	emb, err := os.ReadFile("../testdata/models/embeddings/embeddings.safetensors")
	require.NoError(tb, err)
	ef, err := mf.ParseSafetensors(emb)
	require.NoError(tb, err)
	tn, ok := ef.Tensors["embeddings"]
	require.True(tb, ok)
	require.Len(tb, tn.Shape, 2)
	all, err := tn.Float64s()
	require.NoError(tb, err)
	rows, dim := tn.Shape[0], tn.Shape[1]
	vecs := make([][]float32, rows)
	for i := range vecs {
		vecs[i] = make([]float32, dim)
		for j := range vecs[i] {
			vecs[i][j] = float32(all[i*dim+j])
		}
	}
	s, err := mf.New(trg11Embedder{vecs: vecs}, u)
	require.NoError(tb, err)

	out := filepath.Join(tb.TempDir(), "mf.json")
	trg11Python(tb, "triage_research.reference_mf", "--checkpoint-dir", dir, "--out", out)
	data, err := os.ReadFile(out) //nolint:gosec // G304: test-controlled path
	require.NoError(tb, err)
	var ref struct {
		Scores []struct {
			PMFModel float64 `json:"p_mfmodel"`
			PStrong  float64 `json:"p_strong"`
		} `json:"scores"`
	}
	require.NoError(tb, json.Unmarshal(data, &ref))
	require.Len(tb, ref.Scores, rows)
	for i := range vecs {
		sc, err := s.Score(tb.Context(), trg11Req("e"+strconv.Itoa(i)))
		require.NoError(tb, err)
		maxMFModel = math.Max(maxMFModel, math.Abs(sc.P-ref.Scores[i].PMFModel))
		maxClosed = math.Max(maxClosed, math.Abs(sc.P-ref.Scores[i].PStrong))
	}
	return maxMFModel, maxClosed, rows
}

// trg11Limit checks `got` against a numeric threshold; a non-numeric threshold is report only.
func trg11Limit(th map[string]any, key string) (limit float64, numeric bool) {
	switch v := th[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

// TestAcceptance_TRG11_Parity implements TRG-11.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG11_Parity(t *testing.T) {
	th := thresholds(t, trg11ID)
	env := acceptanceEnv(t)
	res := Result{ID: trg11ID, Measured: map[string]any{"env": env}}
	defer func() {
		if !res.Pass && res.Details == "" {
			res.Details = "aborted before all measurements finished"
		}
		writeResult(t, res)
	}()
	fail := func(format string, a ...any) {
		res.Details = fmt.Sprintf(format, a...)
		t.Fatalf(format, a...)
	}

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	tokDir := os.Getenv("TRIAGE_TOKENIZER_DIR")
	if tokDir == "" {
		tokDir = filepath.Join(home, ".cache", "rayld", "triage", "tokenizers")
	}
	bertTok := filepath.Join(tokDir, "bert", "tokenizer.json")
	if _, err := os.Stat(bertTok); err != nil {
		fail("tokenizer missing (run make bootstrap): %v", err)
	}

	art := trg11Select(t, env)
	res.Measured["artifacts"] = art
	corpus := trg11ReadJSONL[trg11Corpus](t, "../testdata/tokenizer/corpus.jsonl")
	require.Len(t, corpus, 300)
	// 200 prompts: every corpus line whose index is not 2 mod 3, which keeps every category.
	var prompts []trg11Corpus
	for i, c := range corpus {
		if i%3 != 2 {
			prompts = append(prompts, c)
		}
	}
	require.Len(t, prompts, trg11ORTPrompts)
	work := t.TempDir()
	subset := filepath.Join(work, "prompts.jsonl") // absolute: python runs from the repository root
	var sb bytes.Buffer
	for _, p := range prompts {
		line, err := json.Marshal(map[string]string{"id": p.ID, "text": p.Text})
		require.NoError(t, err)
		sb.Write(line)
		sb.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(subset, sb.Bytes(), 0o600))

	ctx := t.Context()

	// (a) tokenizer identity.
	same, total, mism := trg11Tokenizer(t, corpus, tokDir)
	ratio := float64(same) / float64(total)
	res.Measured["tokenizer_identity_ratio"] = ratio
	res.Measured["tokenizer_compared"] = total
	res.Measured["tokenizer_mismatches"] = mism

	// (b) Go ORT vs Python ORT on the same ONNX files.
	rt, err := onnxrt.Open()
	require.NoError(t, err, "ONNX Runtime is required (TRIAGE_ONNX_LIBRARY_PATH)")
	defer func() { require.NoError(t, rt.Close()) }()
	goFP32 := trg11GoBert(ctx, t, rt, bertTok, art.fp32, bert.VariantFP32, prompts)
	goINT8 := trg11GoBert(ctx, t, rt, bertTok, art.int8, bert.VariantINT8, prompts)
	pyScores := func(name string, args ...string) map[string]float64 {
		out := filepath.Join(work, name+".jsonl")
		base := make([]string, 0, 6+len(args))
		base = append(base, "--tokenizer", bertTok, "--prompts", subset, "--out", out)
		trg11Python(t, "triage_research.reference_bert", append(base, args...)...)
		return trg11ByID(trg11ReadJSONL[trg11Scored](t, out))
	}
	pyFP32 := pyScores("onnx_fp32", "--backend", "onnx", "--model", art.graphSource, "--checkpoint-dir", art.checkpointDir)
	pyINT8 := pyScores("onnx_int8", "--backend", "onnx", "--model", art.int8, "--checkpoint-dir", art.checkpointDir)
	dFP32, atFP32 := trg11MaxDiff(t, prompts, goFP32, pyFP32)
	dINT8, atINT8 := trg11MaxDiff(t, prompts, goINT8, pyINT8)
	res.Measured["ort_go_vs_python_fp32_max_abs_dp"] = dFP32
	res.Measured["ort_go_vs_python_fp32_worst_id"] = atFP32
	res.Measured["ort_go_vs_python_int8_max_abs_dp"] = dINT8
	res.Measured["ort_go_vs_python_int8_worst_id"] = atINT8
	res.Measured["ort_go_vs_python_max_abs_dp"] = math.Max(dFP32, dINT8)
	res.Measured["ort_prompts"] = len(prompts)

	// (c) FP32 ONNX vs RouteLLM BERTRouter (torch), (d) INT8 vs torch routing agreement.
	torch := pyScores("torch", "--backend", "torch", "--checkpoint-dir", art.checkpointDir)
	dTorch, atTorch := trg11MaxDiff(t, prompts, goFP32, torch)
	dPyTorch, _ := trg11MaxDiff(t, prompts, pyFP32, torch)
	dINT8Torch, _ := trg11MaxDiff(t, prompts, goINT8, torch)
	agree30 := trg11Agreement(prompts, torch, goINT8, trg11StrongRatioLow)
	agree50 := trg11Agreement(prompts, torch, goINT8, trg11StrongRatioMid)
	res.Measured["fp32_vs_torch_max_abs_dp"] = dTorch
	res.Measured["fp32_vs_torch_worst_id"] = atTorch
	res.Measured["python_fp32_vs_torch_max_abs_dp"] = dPyTorch
	res.Measured["int8_vs_torch_max_abs_dp"] = dINT8Torch
	res.Measured["int8_agreement_strong_0.3"] = agree30
	res.Measured["int8_agreement_strong_0.5"] = agree50
	res.Measured["int8_agreement_min"] = math.Min(agree30, agree50)

	// (e) mf vs RouteLLM MFModel on the recorded embeddings.
	dMF, dMFClosed, nMF := trg11MF(t, art.mfDir)
	res.Measured["mf_max_abs_dp"] = dMF
	res.Measured["mf_vs_closed_form_max_abs_dp"] = dMFClosed
	res.Measured["mf_embeddings"] = nMF

	var failures []string
	upper := func(key string, got float64) {
		if limit, ok := trg11Limit(th, key); ok && got > limit {
			failures = append(failures, fmt.Sprintf("%s = %g > %g", key, got, limit))
		}
	}
	if limit, ok := trg11Limit(th, "tokenizer_identity_ratio"); ok && ratio < limit {
		failures = append(failures, fmt.Sprintf("tokenizer_identity_ratio = %g < %g (%v)", ratio, limit, mism))
	}
	upper("ort_go_vs_python_max_abs_dp", math.Max(dFP32, dINT8))
	upper("fp32_vs_torch_max_abs_dp", dTorch)
	upper("mf_max_abs_dp", dMF)
	if limit, ok := trg11Limit(th, "int8_agreement_min"); ok {
		if got := math.Min(agree30, agree50); got < limit {
			failures = append(failures, fmt.Sprintf("int8_agreement_min = %g < %g", got, limit))
		}
	} else {
		res.Measured["int8_agreement_min_threshold"] = fmt.Sprint(th["int8_agreement_min"], " (", trg11ReportOnly, ")")
	}
	if len(failures) > 0 {
		fail("TRG-11 parity failed on %s artifacts: %s", art.Kind, strings.Join(failures, "; "))
	}
	res.Pass = true
	res.Details = "parity within thresholds on " + art.Kind + " artifacts"
}
