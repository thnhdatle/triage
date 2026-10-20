// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/bench/loadgen"
)

// TRG-08: non-streaming throughput through the router with the length
// strategy (fakellm latency 0, --record=false). Open-model steps of rising
// arrival rate run until one does not sustain: a step sustains when every
// request succeeds, the completed rate is at least 98% of the offered rate
// and, where the environment has a p99 threshold, p99 stays below it. The
// measured throughput is the completed rate of the highest sustained step.
// Requests are the TRG-02 prompts (2,048 characters, two PII items) on a
// mask route, the more expensive path. The same ladder with the bert
// strategy (INT8, real artifacts in models.cache_dir) reports bert's
// saturation throughput through the router; every request must be scored by
// bert (no strategy error fallback). bert is not gated.

const (
	trg08StepDuration = 15 * time.Second
	trg08BertStep     = 10 * time.Second
	trg08Sustain      = 0.98
	// trg08BertP99 bounds bert steps: beyond it the score queue is growing.
	trg08BertP99 = time.Second
)

var (
	trg08Ladder     = []float64{1000, 2000, 3000, 3500, 4000, 5000, 6000, 7000, 8000, 10000, 12000, 15000}
	trg08BertLadder = []float64{5, 10, 20, 30, 40, 60, 80, 100, 130, 160, 200, 250, 300, 400}
)

// step is one constant-rate run.
type step struct {
	Offered   float64 `json:"offered_rps"`
	Completed float64 `json:"completed_rps"`
	P50Ms     float64 `json:"p50_ms"`
	P99Ms     float64 `json:"p99_ms"`
	Errors    int     `json:"errors"`
	Sustained bool    `json:"sustained"`
	FirstErr  string  `json:"first_error,omitempty"`
}

// ladder runs steps of rising rate until one is not sustained and returns
// every step run and the index of the highest sustained one (-1 if none).
func ladder(b *testing.B, rates []float64, d time.Duration, p99Max time.Duration, post loadgen.Do) ([]step, int) {
	b.Helper()
	var steps []step
	best := -1
	for _, rate := range rates {
		res, err := loadgen.Run(context.Background(), loadgen.Config{Rate: rate, Duration: d}, post)
		require.NoError(b, err)
		s := res.Summarize()
		st := step{
			Offered: rate, Completed: s.Throughput, P50Ms: msRound(s.P50), P99Ms: msRound(s.P99),
			Errors: s.Errors + s.Dropped, FirstErr: s.FirstError,
		}
		st.Sustained = st.Errors == 0 && s.Throughput >= trg08Sustain*rate && (p99Max <= 0 || s.P99 < p99Max)
		steps = append(steps, st)
		b.Logf("step %6.0f RPS: completed %8.1f RPS, p50 %7.3f ms, p99 %7.3f ms, errors %d, sustained %v",
			rate, st.Completed, st.P50Ms, st.P99Ms, st.Errors, st.Sustained)
		if !st.Sustained {
			break
		}
		best = len(steps) - 1
		time.Sleep(time.Second) // let connections and queues settle between steps
	}
	return steps, best
}

// stepsSummary renders steps as "offered:completed/p99ms[/errors]" entries
// (a failed step ends with "x" and its first error is appended).
func stepsSummary(steps []step) string {
	parts := make([]string, 0, len(steps))
	last := ""
	for _, s := range steps {
		e := fmt.Sprintf("%.0f:%.1f/%.1fms", s.Offered, s.Completed, s.P99Ms)
		if s.Errors > 0 {
			e += fmt.Sprintf("/%derr", s.Errors)
		}
		if !s.Sustained {
			e += " x"
			last = s.FirstErr
		}
		parts = append(parts, e)
	}
	out := strings.Join(parts, " ")
	if last != "" {
		out += " (" + last + ")"
	}
	return out
}

// trg08Bert runs the bert ladder; the error explains a missing measurement.
func trg08Bert(b *testing.B, f *fake, bodies [][]byte) ([]step, int, error) {
	b.Helper()
	cache := os.Getenv("TRIAGE_MODELS_CACHE_DIR")
	if cache == "" {
		home, err := os.UserHomeDir()
		require.NoError(b, err)
		cache = filepath.Join(home, ".cache", "rayld", "triage", "models")
	}
	if m, _ := filepath.Glob(filepath.Join(cache, "bert", "*", "model.int8.onnx")); len(m) == 0 {
		return nil, -1, fmt.Errorf("no bert INT8 artifact under %s (run make models)", cache)
	}
	cfg := perfConfig(f, "bert", "mask", "  bert: { variant: int8 }\n") + "models: { cache_dir: " + cache + " }\n"
	tr, err := tryStartTriagePerf(b, cfg)
	if err != nil {
		return nil, -1, err
	}
	defer tr.stop(b)
	url := tr.data + "/v1/chat/completions"
	hc := loadgen.NewClient(perfConns, 30*time.Second)
	defer hc.CloseIdleConnections()
	scored := func(h http.Header) error {
		if r := h.Get("X-Triage-Route-Reason"); !strings.Contains(r, "strategy:bert p=") {
			return errors.New("not scored by bert: " + r)
		}
		return nil
	}
	post := func(ctx context.Context, i int) error {
		return loadgen.PostCheck(ctx, hc, url, bodies[i%len(bodies)], scored)
	}
	if err := post(context.Background(), 0); err != nil {
		return nil, -1, fmt.Errorf("bert route: %w", err)
	}
	perfWarmup(b, 5, 2*time.Second, post)
	steps, best := ladder(b, trg08BertLadder, trg08BertStep, trg08BertP99, post)
	return steps, best, nil
}

// BenchmarkAcceptance_TRG08_Throughput implements TRG-08.
func BenchmarkAcceptance_TRG08_Throughput(b *testing.B) {
	thr := thresholds(b, "TRG-08")
	rpsMin := num(b, thr, "rps_min")
	var p99Max time.Duration
	if _, ok := thr["p99_ms_max"]; ok {
		p99Max = time.Duration(num(b, thr, "p99_ms_max") * float64(time.Millisecond))
	}
	f := perfFakellm(b)
	tr := startTriagePerf(b, perfConfig(f, "length", "mask"))
	perfCheckRoute(b, tr, "length", "mask")
	bodies := perfBodies(b, 4096, "perf")
	url := tr.data + "/v1/chat/completions"
	hc := loadgen.NewClient(perfConns, 30*time.Second)
	b.Cleanup(hc.CloseIdleConnections)
	post := func(ctx context.Context, i int) error {
		return loadgen.Post(ctx, hc, url, bodies[i%len(bodies)])
	}
	perfWarmup(b, 500, 3*time.Second, post)

	b.ResetTimer()
	steps, best := ladder(b, trg08Ladder, trg08StepDuration, p99Max, post)
	b.StopTimer()
	tr.stop(b)
	hc.CloseIdleConnections()

	m := map[string]any{"steps": stepsSummary(steps), "step_duration_s": trg08StepDuration.Seconds()}
	var rps float64
	var p99 float64
	if best >= 0 {
		rps, p99 = steps[best].Completed, steps[best].P99Ms
		m["rps"], m["p99_ms"] = math.Round(rps*10)/10, p99
	} else {
		m["rps"] = 0
	}
	if best == len(trg08Ladder)-1 {
		m["note"] = "highest ladder step sustained; throughput is at least this"
	}

	bSteps, bBest, bErr := trg08Bert(b, f, bodies)
	switch {
	case bErr != nil:
		m["bert_saturation"] = "not measured: " + bErr.Error()
	case bBest < 0:
		m["bert_saturation_rps"], m["bert_steps"] = 0, stepsSummary(bSteps)
	default:
		m["bert_saturation_rps"] = math.Round(bSteps[bBest].Completed*10) / 10
		m["bert_p99_ms"], m["bert_steps"] = bSteps[bBest].P99Ms, stepsSummary(bSteps)
		b.ReportMetric(bSteps[bBest].Completed, "bert-rps")
	}

	b.ReportMetric(rps, "rps")
	b.ReportMetric(p99, "p99-ms")
	pass := best >= 0 && rps >= rpsMin && (p99Max <= 0 || p99 < loadgen.Ms(p99Max))
	details := fmt.Sprintf("sustained %.0f RPS with p99 %.3f ms (threshold >= %v RPS", rps, p99, rpsMin)
	if p99Max > 0 {
		details += fmt.Sprintf(", p99 < %v ms", loadgen.Ms(p99Max))
	}
	details += ")"
	if v, ok := m["bert_saturation_rps"]; ok {
		details += fmt.Sprintf("; bert saturation %v RPS (reported)", v)
	} else {
		details += "; bert " + fmt.Sprint(m["bert_saturation"])
	}
	writeResult(b, Result{ID: "TRG-08", Measured: m, Pass: pass, Details: details})
	if !pass {
		b.Fatal(details)
	}
	b.Log(details)
}
