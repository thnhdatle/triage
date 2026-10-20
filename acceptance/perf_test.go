// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/bench/loadgen"
	"github.com/thnhdatle/triage/internal/wire"
)

// Shared helpers of the performance criteria (TRG-02, TRG-08). Triage runs
// in-process (natively on the host); fakellm is a dependency in
// a container. The compose fakellm records every request (--record-limit),
// so the benchmarks start their own fakellm container from the same image
// with --record=false and remove it when they finish.

const (
	perfFakeImageVar = "RAYLD_FAKELLM_IMAGE"
	perfFakeImage    = "thnhdatle/fakellm:dev"
	// perfFakeURLVar points the benchmarks at an already running fakellm
	// started with --record=false instead of starting a container.
	perfFakeURLVar = "TRIAGE_BENCH_FAKELLM_URL"
	// perfPromptChars is 512 tokens at 4 ASCII characters per token.
	perfPromptChars = 2048
	perfConns       = 2048
)

// perfFakellm returns a fakellm with request recording off.
func perfFakellm(tb testing.TB) *fake {
	tb.Helper()
	hc := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	tb.Cleanup(hc.CloseIdleConnections)
	if u := os.Getenv(perfFakeURLVar); u != "" {
		return &fake{url: strings.TrimSuffix(u, "/"), hc: hc}
	}
	image := os.Getenv(perfFakeImageVar)
	if image == "" {
		image = perfFakeImage
	}
	name := fmt.Sprintf("triage-bench-fakellm-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, //nolint:gosec,noctx // fixed args
		"-p", "127.0.0.1::8090", image, "--record=false").CombinedOutput()
	require.NoError(tb, err, "start %s: %s", image, out)
	tb.Cleanup(func() {
		rm := exec.Command("docker", "rm", "-f", name) //nolint:gosec,noctx // fixed args
		if out, err := rm.CombinedOutput(); err != nil {
			tb.Logf("remove %s: %v: %s", name, err, out)
		}
	})
	out, err = exec.Command("docker", "port", name, "8090/tcp").Output() //nolint:gosec,noctx // fixed args
	require.NoError(tb, err)
	addr := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	f := &fake{url: "http://" + addr, hc: hc}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := hc.Get(f.url + "/healthz") //nolint:noctx // bounded by the client timeout
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return f
			}
		}
		require.True(tb, time.Now().Before(deadline), "fakellm container %s not healthy: %v", name, err)
		time.Sleep(100 * time.Millisecond)
	}
}

// startTriagePerf serves cfgYAML in-process like startTriage, but as
// production runs it: tracing off (no in-memory exporter, OTEL_* ignored)
// and logs discarded.
func startTriagePerf(tb testing.TB, cfgYAML string) *triage {
	tb.Helper()
	tr, err := tryStartTriagePerf(tb, cfgYAML)
	require.NoError(tb, err)
	return tr
}

// tryStartTriagePerf is startTriagePerf returning a start failure.
func tryStartTriagePerf(tb testing.TB, cfgYAML string) (*triage, error) {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "triage.yaml")
	require.NoError(tb, os.WriteFile(path, []byte(cfgYAML), 0o600))
	addrs := make(chan [2]net.Addr, 1)
	o := wire.ServeOptions{
		ConfigPath: path, WatchInterval: -1, ShutdownTimeout: 10 * time.Second,
		Env: wire.Env{Getenv: func(k string) string {
			if strings.HasPrefix(k, "OTEL_") {
				return ""
			}
			return os.Getenv(k)
		}},
		Started: func(d, a net.Addr) { addrs <- [2]net.Addr{d, a} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr := &triage{cancel: cancel, done: make(chan error, 1), cfgPath: path}
	go func() { tr.done <- wire.Serve(ctx, o) }()
	select {
	case a := <-addrs:
		tr.data, tr.admin = "http://"+a[0].String(), "http://"+a[1].String()
	case err := <-tr.done:
		cancel()
		return nil, fmt.Errorf("serve failed to start: %w", err)
	}
	tr.hc = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 16}, Timeout: 60 * time.Second}
	tb.Cleanup(func() { tr.stop(tb) })
	return tr, nil
}

// perfConfig is a single route "perf" over two fakellm targets whose
// upstream models are perf-small (local, weak) and perf-strong (cloud,
// strong). strategy "" leaves the route unscored (default tier strong).
// The length midpoint of 256 tokens sends the 512-token prompts to the
// strong cloud target, so mask applies on every request.
func perfConfig(f *fake, strategy, privacyMode string, difficulty ...string) string {
	cfg := `
server: { listen: "127.0.0.1:0" }
admin: { listen: "127.0.0.1:0" }
auth: { enabled: false }
api: { expose_decision_detail: true }
providers:
  fake: { type: openaicompat, base_url: "` + f.baseURL() + `",
          max_idle_conns_per_host: ` + strconv.Itoa(perfConns) + ` }
health: { interval: 1h }
targets:
  - { name: local-small, provider: fake, model: perf-small, locality: local, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode], pricing: { input_per_mtok: 0, output_per_mtok: 0 } }
  - { name: cloud-strong, provider: fake, model: perf-strong, locality: cloud, tier: strong,
      context_window: 200000, capabilities: [tools, json_mode],
      pricing: { input_per_mtok: 1.25, output_per_mtok: 10.0 } }
difficulty:
  length: { midpoint_tokens: 256, slope: 0.004 }
` + strings.Join(difficulty, "") + `routes:
  - name: perf
    pool: [local-small, cloud-strong]
    privacy: { mode: ` + privacyMode + ` }
`
	if strategy == "" {
		return cfg + "    default_tier: strong\n"
	}
	return cfg + "    strategy: " + strategy + "\n    threshold: 0.5\n"
}

// perfFiller is the prose the prompts are padded with.
var perfFiller = strings.Fields(`the router reads each request and decides which model should answer it based on
the estimated difficulty of the prompt while keeping private data away from cloud targets so please summarise
the following notes about the quarterly planning meeting and list the open questions with owners and due dates`)

// perfPrompt is prompt i of the schedule: exactly perfPromptChars ASCII
// characters holding two PII items (an e-mail address and a Luhn-valid Visa
// number), both unique per i, at positions that vary with i.
func perfPrompt(i int) string {
	rng := rand.New(rand.NewPCG(uint64(i), 0x7472672d3032)) //nolint:gosec // deterministic test data
	card := luhnCard(fmt.Sprintf("4%014d", 31_000_000_000_000+uint64(i)*7919%1_000_000_000_000))
	pii0 := "Contact me at user" + strconv.Itoa(i) + ".bench@example.com about it."
	pii1 := "Charge card " + card + " for the order."
	n := perfPromptChars - len(pii0) - len(pii1) - 4
	var fb strings.Builder
	for fb.Len() < n {
		fb.WriteString(perfFiller[rng.IntN(len(perfFiller))])
		fb.WriteByte(' ')
	}
	filler := fb.String()[:n]
	a := n/8 + rng.IntN(n/4)
	b := n/2 + rng.IntN(n/4)
	return filler[:a] + " " + pii0 + " " + filler[a:b] + " " + pii1 + " " + filler[b:]
}

// luhnCard appends the Luhn check digit to digits.
func luhnCard(digits string) string {
	sum := 0
	for k := range len(digits) {
		d := int(digits[len(digits)-1-k] - '0')
		if k%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return digits + strconv.Itoa((10-sum%10)%10)
}

// perfBodies pre-encodes n chat bodies for model.
func perfBodies(tb testing.TB, n int, model string) [][]byte {
	tb.Helper()
	out := make([][]byte, n)
	for i := range out {
		b, err := json.Marshal(chatBody(model, perfPrompt(i), false))
		require.NoError(tb, err)
		out[i] = b
	}
	return out
}

// perfCheckRoute verifies once that the route does what the scenario says:
// strategy decided, strong cloud target, both PII items masked.
func perfCheckRoute(tb testing.TB, tr *triage, strategy, mode string) {
	tb.Helper()
	ctx := context.Background()
	r, err := tr.chat(ctx, chatBody("perf", perfPrompt(0), false))
	require.NoError(tb, err)
	require.Equal(tb, http.StatusOK, r.status, string(r.body))
	require.Equal(tb, "cloud-strong", r.header.Get("X-Triage-Target"))
	d, _, err := tr.decision(ctx, r.header.Get("X-Triage-Decision"))
	require.NoError(tb, err)
	require.Equal(tb, strategy, d.Strategy, "decision %+v", d)
	require.Equal(tb, mode, d.Privacy)
	if mode == "mask" {
		require.Equal(tb, map[string]int{"credit_card": 1, "email": 1}, d.Findings)
		require.NotEmpty(tb, d.Attempts)
		require.True(tb, d.Attempts[0].Masked, "attempt not masked: %+v", d.Attempts)
	}
}

// perfWarmup sends rate RPS for d to post, failing on any error.
func perfWarmup(tb testing.TB, rate float64, d time.Duration, post loadgen.Do) {
	tb.Helper()
	res, err := loadgen.Run(context.Background(), loadgen.Config{Rate: rate, Duration: d}, post)
	require.NoError(tb, err)
	s := res.Summarize()
	require.Zero(tb, s.Errors+s.Dropped, "warm-up: %s", s.FirstError)
}

// msRound rounds milliseconds to 3 decimals for the result file.
func msRound(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// BenchmarkPerf_TRG02_OverheadNoScoring is the TRG-02 schedule on a route
// without a strategy (default tier strong, privacy mask): the router
// overhead excluding scoring, reported by make bench (not a criterion).
func BenchmarkPerf_TRG02_OverheadNoScoring(b *testing.B) {
	thr := thresholds(b, "TRG-02")
	rate, secs := num(b, thr, "rps"), num(b, thr, "duration_s")
	o := measureOverhead(b, "", rate, time.Duration(secs*float64(time.Second)))
	o.report(b)
	require.Zero(b, o.errors, o.firstError)
	b.Logf("no scoring: p99 paired overhead %.3f ms, p50 %.3f ms over %d requests",
		loadgen.Ms(o.p99), loadgen.Ms(o.p50), o.n)
}
