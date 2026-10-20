// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/bench/loadgen"
)

// TRG-02: router overhead with the length strategy and privacy mask at
// 500 RPS for 60 s. Every scheduled request i is sent at the same instant
// twice, directly to fakellm and through Triage (which forwards it to the
// same fakellm), so both arms see the same schedule and the same machine
// noise; the overhead is the p99 of the paired differences
// (Triage latency minus direct latency). fakellm answers with latency 0 and
// does not record requests. 2,048-character prompts hold two PII items each,
// and the 512-token length score routes them to the cloud target, so every
// request is scanned and masked and its answer unmasked.

const trg02Warmup = 5 * time.Second

// overhead is the outcome of one paired run.
type overhead struct {
	n                    int
	p50, p99, max        time.Duration // paired differences
	directP99, triageP99 time.Duration
	directP50, triageP50 time.Duration
	unpairedP99, maxLag  time.Duration
	errors               int
	firstError           string
}

// measureOverhead runs the paired schedule against a Triage serving a route
// with strategy ("" = unscored) and privacy mask.
func measureOverhead(b *testing.B, strategy string, rate float64, dur time.Duration) overhead {
	b.Helper()
	f := perfFakellm(b)
	tr := startTriagePerf(b, perfConfig(f, strategy, "mask"))
	perfCheckRoute(b, tr, strategy, "mask")

	cfg := loadgen.Config{Rate: rate, Duration: dur}
	n := cfg.Requests()
	viaTriage := perfBodies(b, n, "perf")
	direct := perfBodies(b, n, "perf-strong")
	hcTriage, hcDirect := loadgen.NewClient(perfConns, 30*time.Second), loadgen.NewClient(perfConns, 30*time.Second)
	b.Cleanup(hcTriage.CloseIdleConnections)
	b.Cleanup(hcDirect.CloseIdleConnections)
	triageURL, directURL := tr.data+"/v1/chat/completions", f.baseURL()+"/chat/completions"

	type pair struct {
		direct, triage time.Duration
		errD, errT     error
	}
	pairs := make([]pair, n)
	paired := func(ctx context.Context, i int) error {
		start := time.Now()
		done := make(chan struct{})
		go func() {
			defer close(done)
			pairs[i].errT = loadgen.Post(ctx, hcTriage, triageURL, viaTriage[i])
			pairs[i].triage = time.Since(start)
		}()
		pairs[i].errD = loadgen.Post(ctx, hcDirect, directURL, direct[i])
		pairs[i].direct = time.Since(start)
		<-done
		if pairs[i].errT != nil {
			return pairs[i].errT
		}
		return pairs[i].errD
	}
	perfWarmup(b, rate, trg02Warmup, func(ctx context.Context, i int) error { return paired(ctx, i%n) })
	clear(pairs)

	b.ResetTimer()
	res, err := loadgen.Run(context.Background(), cfg, paired)
	b.StopTimer()
	require.NoError(b, err)

	out := overhead{maxLag: res.MaxLag}
	var diffs, d, t []time.Duration
	for i, p := range pairs {
		if res.Samples[i].Dropped || p.errD != nil || p.errT != nil {
			out.errors++
			if out.firstError == "" {
				out.firstError = fmt.Sprintf("request %d: dropped=%v direct=%v triage=%v",
					i, res.Samples[i].Dropped, p.errD, p.errT)
			}
			continue
		}
		diffs = append(diffs, p.triage-p.direct)
		d, t = append(d, p.direct), append(t, p.triage)
	}
	out.n = len(diffs)
	out.p50, out.p99 = loadgen.Percentile(diffs, 0.5), loadgen.Percentile(diffs, 0.99)
	out.max = loadgen.Percentile(diffs, 1)
	out.directP50, out.directP99 = loadgen.Percentile(d, 0.5), loadgen.Percentile(d, 0.99)
	out.triageP50, out.triageP99 = loadgen.Percentile(t, 0.5), loadgen.Percentile(t, 0.99)
	out.unpairedP99 = out.triageP99 - out.directP99
	return out
}

func (o overhead) measured() map[string]any {
	return map[string]any{
		"requests":              o.n,
		"errors":                o.errors,
		"p99_overhead_ms":       msRound(o.p99),
		"p50_overhead_ms":       msRound(o.p50),
		"max_overhead_ms":       msRound(o.max),
		"direct_p50_ms":         msRound(o.directP50),
		"direct_p99_ms":         msRound(o.directP99),
		"triage_p50_ms":         msRound(o.triageP50),
		"triage_p99_ms":         msRound(o.triageP99),
		"unpaired_p99_delta_ms": msRound(o.unpairedP99),
		"max_dispatch_lag_ms":   msRound(o.maxLag),
	}
}

func (o overhead) report(b *testing.B) {
	b.Helper()
	b.ReportMetric(loadgen.Ms(o.p99), "p99-overhead-ms")
	b.ReportMetric(loadgen.Ms(o.p50), "p50-overhead-ms")
	b.ReportMetric(loadgen.Ms(o.triageP99), "p99-triage-ms")
	b.ReportMetric(loadgen.Ms(o.directP99), "p99-direct-ms")
	b.ReportMetric(float64(o.errors), "errors")
}

// BenchmarkAcceptance_TRG02_Overhead implements TRG-02.
func BenchmarkAcceptance_TRG02_Overhead(b *testing.B) {
	thr := thresholds(b, "TRG-02")
	rate, secs, maxMs := num(b, thr, "rps"), num(b, thr, "duration_s"), num(b, thr, "p99_overhead_ms_max")
	o := measureOverhead(b, "length", rate, time.Duration(secs*float64(time.Second)))
	o.report(b)
	pass := o.errors == 0 && o.n > 0 && loadgen.Ms(o.p99) < maxMs
	details := fmt.Sprintf("p99 paired overhead %.3f ms (threshold < %v ms) over %d requests at %v RPS for %v s",
		loadgen.Ms(o.p99), maxMs, o.n, rate, secs)
	if o.errors > 0 {
		details += fmt.Sprintf("; %d failed requests, first: %s", o.errors, o.firstError)
	}
	writeResult(b, Result{ID: "TRG-02", Measured: o.measured(), Pass: pass, Details: details})
	if !pass {
		b.Fatal(details)
	}
	b.Log(details)
}
