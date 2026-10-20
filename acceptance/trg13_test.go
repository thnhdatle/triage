// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/bench/prompts"
	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/platform/cpu"
	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

const (
	trg13ID          = "TRG-13"
	trg13IDs         = "../bench/prompts/trg13.txt"
	trg13Summary     = "../bench/trg13/result.json"
	trg13Runs        = 3 // each scenario runs 3 times
	trg13Threads     = 4 // bert.intra_op_threads
	trg13QueueSize   = bert.DefaultQueueSize
	trg13Warmup      = 20
	trg13Tokens256   = 256
	trg13Seq256Calls = 200 // one at a time, prompts truncated to exactly 256 tokens
	trg13SatCallers  = 16  // closed-loop callers for saturation (more than slots + queue pressure)
	trg13Sat256Calls = 600
)

// trg13Stats summarises latencies in milliseconds.
type trg13Stats struct {
	N    int     `json:"n"`
	P50  float64 `json:"p50_ms"`
	P90  float64 `json:"p90_ms"`
	P99  float64 `json:"p99_ms"`
	Max  float64 `json:"max_ms"`
	Mean float64 `json:"mean_ms"`
}

func trg13Summarise(lat []time.Duration) trg13Stats {
	ms := make([]float64, len(lat))
	var sum float64
	for i, d := range lat {
		ms[i] = float64(d) / float64(time.Millisecond)
		sum += ms[i]
	}
	slices.Sort(ms)
	return trg13Stats{
		N: len(ms), P50: trg13Quantile(ms, 0.50), P90: trg13Quantile(ms, 0.90), P99: trg13Quantile(ms, 0.99),
		Max: ms[len(ms)-1], Mean: sum / float64(len(ms)),
	}
}

// trg13Quantile is the nearest-rank quantile of sorted values.
func trg13Quantile(sorted []float64, q float64) float64 {
	k := max(int(math.Ceil(q*float64(len(sorted))))-1, 0)
	return sorted[k]
}

// trg13Spread is the median, min and max of one metric over the runs.
type trg13Spread struct {
	Median float64   `json:"median"`
	Min    float64   `json:"min"`
	Max    float64   `json:"max"`
	Runs   []float64 `json:"runs"`
}

func trg13SpreadOf(vals []float64) trg13Spread {
	s := slices.Clone(vals)
	slices.Sort(s)
	return trg13Spread{Median: s[len(s)/2], Min: s[0], Max: s[len(s)-1], Runs: vals}
}

// trg13Run is one repetition of every scenario.
type trg13Run struct {
	OpenModel       trg13Stats `json:"open_model_5rps"`
	OpenModelLate   float64    `json:"open_model_max_dispatch_lag_ms"`
	Seq256          trg13Stats `json:"sequential_256_tokens"`
	SaturationRPS   float64    `json:"saturation_rps_trg13_set"`
	Saturation256   float64    `json:"saturation_rps_256_tokens"`
	SaturationStats trg13Stats `json:"saturation_latency_trg13_set"`
}

// trg13Report is bench/trg13/result.json.
type trg13Report struct {
	Criterion  string         `json:"criterion"`
	Date       string         `json:"date"`
	Version    string         `json:"version"`
	Machine    string         `json:"machine"`
	GoVersion  string         `json:"go_version"`
	ORTVersion string         `json:"ort_version"`
	Model      string         `json:"model"`
	Setup      map[string]any `json:"setup"`
	Prompts    map[string]any `json:"prompts"`
	Threshold  map[string]any `json:"threshold"`
	P50        trg13Spread    `json:"open_model_p50_ms"`
	P99        trg13Spread    `json:"open_model_p99_ms"`
	Seq256P50  trg13Spread    `json:"sequential_256_tokens_p50_ms"`
	Seq256P99  trg13Spread    `json:"sequential_256_tokens_p99_ms"`
	SatRPS     trg13Spread    `json:"saturation_rps_trg13_set"`
	Sat256RPS  trg13Spread    `json:"saturation_rps_256_tokens"`
	Runs       []trg13Run     `json:"runs"`
	Pass       bool           `json:"pass"`
	GateMetric string         `json:"gate"`
}

// trg13Artifacts locates the pulled real bert artifacts (`triage models pull bert --int8`).
func trg13Artifacts(tb testing.TB) (dir string) {
	tb.Helper()
	home, err := os.UserHomeDir()
	require.NoError(tb, err)
	found, _ := filepath.Glob(filepath.Join(home, ".cache", "rayld", "triage", "models", "bert", "*", bert.INT8FileName))
	require.Len(tb, found, 1, "TRG-13 needs the real INT8 bert artifact: run `triage models pull bert --int8`")
	dir = filepath.Dir(found[0])
	_, err = os.Stat(filepath.Join(dir, "tokenizer.json"))
	require.NoError(tb, err, "tokenizer.json next to %s", found[0])
	return dir
}

// trg13DataDir is the converted eval data of scripts/fetch_eval_data.sh.
func trg13DataDir(tb testing.TB) string {
	tb.Helper()
	if d := os.Getenv("EVAL_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	require.NoError(tb, err)
	return filepath.Join(home, ".cache", "rayld", "triage", "eval", "data")
}

func trg13Req(text string) chat.Request {
	return chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: text}}},
	}}
}

// trg13Load loads the INT8 strategy the way the router sizes it (internal/wire bertConfig with the
// default pool sizing: 4 intra-op threads, physical cores ÷ 4 concurrent runs, queue 64).
func trg13Load(tb testing.TB, rt *onnxrt.Runtime, dir string, maxTokens int) *bert.Strategy {
	tb.Helper()
	s, err := bert.Load(tb.Context(), rt, bert.Config{
		TokenizerPath: filepath.Join(dir, "tokenizer.json"), ModelPath: filepath.Join(dir, bert.INT8FileName),
		Variant: bert.VariantINT8, MaxInputTokens: maxTokens, IntraOpThreads: trg13Threads,
		MaxConcurrency: trg13Concurrency(), QueueSize: trg13QueueSize,
	})
	require.NoError(tb, err)
	return s
}

func trg13Concurrency() int { return max(1, cpu.PhysicalCores()/trg13Threads) }

// trg13OpenModel scores every request once at a constant arrival rate (open model: each call
// starts at its scheduled time whatever the state of earlier calls) and returns the latencies,
// measured from the scheduled start so that dispatch lag counts against the strategy.
func trg13OpenModel(
	tb testing.TB, s *bert.Strategy, reqs []chat.Request, rps float64,
) ([]time.Duration, time.Duration) {
	tb.Helper()
	ctx := tb.Context()
	interval := time.Duration(float64(time.Second) / rps)
	lat := make([]time.Duration, len(reqs))
	errs := make([]error, len(reqs))
	var (
		wg     sync.WaitGroup
		maxLag time.Duration
	)
	start := time.Now().Add(interval)
	for i, req := range reqs {
		at := start.Add(time.Duration(i) * interval)
		time.Sleep(time.Until(at))
		maxLag = max(maxLag, time.Since(at))
		wg.Go(func() {
			_, errs[i] = s.Score(ctx, req)
			lat[i] = time.Since(at)
		})
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(tb, err, "request %d", i)
	}
	return lat, maxLag
}

// trg13Sequential scores the requests one at a time.
func trg13Sequential(tb testing.TB, s *bert.Strategy, reqs []chat.Request) []time.Duration {
	tb.Helper()
	lat := make([]time.Duration, len(reqs))
	for i, req := range reqs {
		t0 := time.Now()
		_, err := s.Score(tb.Context(), req)
		require.NoError(tb, err)
		lat[i] = time.Since(t0)
	}
	return lat
}

// trg13Saturate scores every request with trg13SatCallers closed-loop callers and returns the
// completed calls per second and the per-call latencies.
func trg13Saturate(tb testing.TB, s *bert.Strategy, reqs []chat.Request) (float64, []time.Duration) {
	tb.Helper()
	ctx := tb.Context()
	lat := make([]time.Duration, len(reqs))
	errs := make([]error, len(reqs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range trg13SatCallers {
		wg.Go(func() {
			for i := range next {
				t0 := time.Now()
				_, errs[i] = s.Score(ctx, reqs[i])
				lat[i] = time.Since(t0)
			}
		})
	}
	t0 := time.Now()
	for i := range reqs {
		next <- i
	}
	close(next)
	wg.Wait()
	elapsed := time.Since(t0)
	for i, err := range errs {
		require.NoError(tb, err, "request %d", i)
	}
	return float64(len(reqs)) / elapsed.Seconds(), lat
}

// trg13Prompts resolves the committed ids and returns the requests, the requests whose prompt has
// at least 256 tokens (for the 256-token scenarios) and a description of the set.
func trg13Prompts(tb testing.TB, dir string, want int) (all, long []chat.Request, info map[string]any) {
	tb.Helper()
	ids, err := prompts.ReadIDs(trg13IDs)
	require.NoError(tb, err)
	require.Len(tb, ids, want, "bench/prompts/trg13.txt")
	ps, err := prompts.NewData(trg13DataDir(tb)).Resolve(ids)
	require.NoError(tb, err, "run scripts/fetch_eval_data.sh")
	tok, err := hftok.Load(filepath.Join(dir, "tokenizer.json"))
	require.NoError(tb, err)
	var hist [2]int
	minT, maxT, truncated := math.MaxInt, 0, 0
	for _, p := range ps {
		ids, err := tok.Encode(p.Text, true)
		require.NoError(tb, err)
		n := len(ids)
		minT, maxT = min(minT, n), max(maxT, n)
		switch {
		case n > bert.MaxInputTokens:
			truncated++
		case n >= trg13Tokens256:
			hist[1]++
		default:
			hist[0]++
		}
		all = append(all, trg13Req(p.Text))
		if n >= trg13Tokens256 {
			long = append(long, trg13Req(p.Text))
		}
	}
	require.GreaterOrEqual(tb, minT, 128, "prompt shorter than 128 tokens")
	require.LessOrEqual(tb, maxT, 1024, "prompt longer than 1,024 tokens")
	info = map[string]any{
		"ids": "bench/prompts/trg13.txt", "count": len(ps), "min_tokens": minT, "max_tokens": maxT,
		"tokens_128_255": hist[0], "tokens_256_512": hist[1], "tokens_over_512_truncated": truncated,
	}
	return all, long, info
}

func trg13Cycle(reqs []chat.Request, n int) []chat.Request {
	out := make([]chat.Request, n)
	for i := range out {
		out[i] = reqs[i%len(reqs)]
	}
	return out
}

func trg13Machine(ctx context.Context) string {
	brand, _ := exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string").Output()
	return fmt.Sprintf("%s %s/%s, %d physical cores", strings.TrimSpace(string(brand)), runtime.GOOS, runtime.GOARCH,
		cpu.PhysicalCores())
}

// trg13Version is the Triage version recorded with the result (set by the acceptance runner and
// by `make bench`).
func trg13Version() string {
	if v := strings.TrimSpace(os.Getenv("TRIAGE_ACCEPTANCE_VERSION")); v != "" {
		return v
	}
	return "dev"
}

// trg13Once runs every scenario once.
func trg13Once(b *testing.B, rt *onnxrt.Runtime, dir string, all, long []chat.Request, rps float64) trg13Run {
	b.Helper()
	var r trg13Run
	s := trg13Load(b, rt, dir, bert.MaxInputTokens)
	_ = trg13Sequential(b, s, all[:trg13Warmup])
	lat, lag := trg13OpenModel(b, s, all, rps)
	r.OpenModel, r.OpenModelLate = trg13Summarise(lat), float64(lag)/float64(time.Millisecond)
	b.Logf("open model %.0f RPS: %+v (max dispatch lag %.2f ms)", rps, r.OpenModel, r.OpenModelLate)
	rate, satLat := trg13Saturate(b, s, all)
	r.SaturationRPS, r.SaturationStats = rate, trg13Summarise(satLat)
	b.Logf("saturation, TRG-13 set: %.1f req/s", rate)
	require.NoError(b, s.Close(context.Background()))

	s256 := trg13Load(b, rt, dir, trg13Tokens256)
	defer func() { require.NoError(b, s256.Close(context.Background())) }()
	_ = trg13Sequential(b, s256, long[:trg13Warmup])
	r.Seq256 = trg13Summarise(trg13Sequential(b, s256, trg13Cycle(long, trg13Seq256Calls)))
	b.Logf("sequential, 256 tokens: %+v", r.Seq256)
	r.Saturation256, _ = trg13Saturate(b, s256, trg13Cycle(long, trg13Sat256Calls))
	b.Logf("saturation, 256 tokens: %.1f req/s", r.Saturation256)
	return r
}

// BenchmarkAcceptance_TRG13_StrategyLatency implements TRG-13: bert INT8 scoring latency in-process
// (Score incl. tokenisation and pool queueing) over 1,000 public prompts at an open-model 5 RPS,
// plus the 256-token latency and saturation throughput (reported, not gated). Every scenario runs
// trg13Runs times; the gate holds only if every run's p99 is under the threshold.
func BenchmarkAcceptance_TRG13_StrategyLatency(b *testing.B) {
	thr := thresholds(b, trg13ID)
	nPrompts, rps, p99Max := int(num(b, thr, "prompts")), num(b, thr, "rps"), num(b, thr, "p99_ms_max")
	dir := trg13Artifacts(b)
	all, long, info := trg13Prompts(b, dir, nPrompts)
	rt, err := onnxrt.Open()
	require.NoError(b, err)
	defer func() { require.NoError(b, rt.Close()) }()

	var runs []trg13Run
	for b.Loop() {
		for range trg13Runs {
			runs = append(runs, trg13Once(b, rt, dir, all, long, rps))
		}
	}
	pick := func(f func(trg13Run) float64) trg13Spread {
		v := make([]float64, len(runs))
		for i, r := range runs {
			v[i] = f(r)
		}
		return trg13SpreadOf(v)
	}
	rep := trg13Report{
		Criterion: trg13ID, Date: time.Now().UTC().Format(time.RFC3339), Version: trg13Version(),
		Machine: trg13Machine(b.Context()), GoVersion: runtime.Version(), ORTVersion: rt.Version(),
		Model: "routellm/bert_gpt4_augmented@" + filepath.Base(dir) + " INT8 (" + bert.INT8FileName + ")",
		Setup: map[string]any{
			"intra_op_threads": trg13Threads, "max_concurrency": trg13Concurrency(), "queue_size": trg13QueueSize,
			"max_input_tokens": bert.MaxInputTokens, "rps": rps, "runs": trg13Runs, "warmup_calls": trg13Warmup,
			"saturation_callers": trg13SatCallers, "latency": "scheduled start to Score return (open model)",
		},
		Prompts:   info,
		Threshold: thr,
		P50:       pick(func(r trg13Run) float64 { return r.OpenModel.P50 }),
		P99:       pick(func(r trg13Run) float64 { return r.OpenModel.P99 }),
		Seq256P50: pick(func(r trg13Run) float64 { return r.Seq256.P50 }),
		Seq256P99: pick(func(r trg13Run) float64 { return r.Seq256.P99 }),
		SatRPS:    pick(func(r trg13Run) float64 { return r.SaturationRPS }),
		Sat256RPS: pick(func(r trg13Run) float64 { return r.Saturation256 }),
		Runs:      runs,
	}
	rep.Pass = rep.P99.Max < p99Max
	rep.GateMetric = fmt.Sprintf("open-model p99 < %g ms in every one of %d runs", p99Max, trg13Runs)
	trg13Write(b, rep)
	b.ReportMetric(rep.P99.Median, "p99-ms")
	b.ReportMetric(rep.P99.Max, "p99-max-ms")
	b.ReportMetric(rep.P50.Median, "p50-ms")
	b.ReportMetric(rep.Seq256P50.Median, "p50-256tok-ms")
	b.ReportMetric(rep.SatRPS.Median, "sat-req/s")
	b.ReportMetric(rep.Sat256RPS.Median, "sat256-req/s")
	if !rep.Pass {
		b.Fatalf("TRG-13: open-model p99 %v ms (max %.1f) not < %g ms", rep.P99.Runs, rep.P99.Max, p99Max)
	}
}

// trg13Write writes acceptance/logs/TRG-13.json and bench/trg13/result.json.
func trg13Write(tb testing.TB, rep trg13Report) {
	tb.Helper()
	writeResult(tb, Result{
		ID: trg13ID, Pass: rep.Pass,
		Measured: map[string]any{
			"prompts": rep.Prompts["count"], "rps": rep.Setup["rps"], "runs": trg13Runs,
			"p50_ms": rep.P50.Median, "p99_ms": rep.P99.Median, "p99_ms_max": rep.P99.Max,
			"p99_ms_runs":        rep.P99.Runs,
			"latency_256_p50_ms": rep.Seq256P50.Median, "latency_256_p99_ms": rep.Seq256P99.Median,
			"saturation_rps": rep.SatRPS.Median, "saturation_rps_256": rep.Sat256RPS.Median,
		},
		Details: rep.GateMetric + "; full report in bench/trg13/result.json",
	})
	data, err := json.MarshalIndent(rep, "", "  ")
	require.NoError(tb, err)
	require.NoError(tb, os.MkdirAll(filepath.Dir(trg13Summary), 0o750))
	require.NoError(tb, os.WriteFile(trg13Summary, append(data, '\n'), 0o600))
}
