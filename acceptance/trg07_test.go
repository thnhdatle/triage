// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	trg07Route    = "triage-auto"
	trg07BadRoute = "invalid-route"
	trg07Reloads  = 100
	trg07Every    = 500 * time.Millisecond
	trg07Rate     = 200
)

// trg07Config builds a configuration whose answer marker is "<marker>".
// kind selects the defect of an invalid configuration; "" is valid.
func trg07Config(f *fake, marker, kind string) string {
	hdr := `{ X-Fake-Answer: "` + marker + `" }`
	cfg := targetsYAML(f, "fake", hdr, hdr) + `
routes:
  - name: ` + trg07Route + `
    pool: [local-small, cloud-strong]
    strategy: length
    threshold: 0.5
    on_tier_unavailable: escalate
    fallback: [tier:strong, tier:weak]
difficulty:
  length: { midpoint_tokens: 2048, slope: 0.004 }
`
	switch kind {
	case "":
	case "unknown_target":
		cfg += "  - name: " + trg07BadRoute + "\n    pool: [no-such-target]\n    strategy: length\n    threshold: 0.5\n"
	case "unknown_strategy":
		cfg += "  - name: " + trg07BadRoute +
			"\n    pool: [local-small, cloud-strong]\n    strategy: no-such-strategy\n    threshold: 0.5\n"
	default: // "syntax"
		cfg += "  - name: " + trg07BadRoute + "\n    pool: [local-small\n"
	}
	return cfg
}

// trg07Write replaces the config file atomically.
func trg07Write(tb testing.TB, path, cfg string) {
	tb.Helper()
	tmp := path + ".tmp"
	require.NoError(tb, os.WriteFile(tmp, []byte(cfg), 0o600))
	require.NoError(tb, os.Rename(tmp, filepath.Clean(path)))
}

type trg07Sample struct {
	status   int
	err      error
	decision string
	text     string
	stream   bool
}

var trg07Marker = regexp.MustCompile(`(gen|inv)-(\d+)`)

// trg07Text extracts the answer text of a chat response.
func trg07Text(r response, stream bool) (string, error) {
	if stream {
		s, err := parseStream(r.body)
		return s.text, err
	}
	var c chatResponse
	err := json.Unmarshal(r.body, &c)
	if err == nil && len(c.Choices) == 0 {
		err = errors.New("no choices")
	}
	if err != nil {
		return "", fmt.Errorf("non-stream body %.120s: %w", r.body, err)
	}
	return c.Choices[0].Message.Content, nil
}

// trg07Load is the open-loop request generator.
type trg07Load struct {
	tr       *triage
	mu       sync.Mutex
	samples  []trg07Sample
	wg       sync.WaitGroup
	stop     chan struct{}
	done     chan struct{}
	launched int
	loadDur  time.Duration
	maxLag   time.Duration
}

func (l *trg07Load) stopped() bool {
	select {
	case <-l.stop:
		return true
	default:
		return false
	}
}

// run launches request k at start+k/rate whatever the responses do,
// alternating streaming and non-streaming.
func (l *trg07Load) run(ctx context.Context) {
	defer close(l.done)
	interval := time.Second / trg07Rate
	start := time.Now()
	var last time.Time
	for k := 0; ; k++ {
		at := start.Add(time.Duration(k) * interval)
		if d := time.Until(at); d > 0 {
			select {
			case <-l.stop:
			case <-time.After(d):
			}
		}
		if l.stopped() {
			l.loadDur = last.Sub(start) + interval
			return
		}
		last = time.Now()
		l.maxLag = max(l.maxLag, last.Sub(at))
		l.launched++
		l.wg.Add(1)
		go l.one(ctx, k)
	}
}

func (l *trg07Load) one(ctx context.Context, k int) {
	defer l.wg.Done()
	stream := k%2 == 1
	r, err := l.tr.chat(ctx, chatBody(trg07Route, "hello "+strconv.Itoa(k), stream))
	s := trg07Sample{status: r.status, err: err, decision: r.header.Get("X-Triage-Decision"), stream: stream}
	if err == nil && r.status == http.StatusOK {
		s.text, s.err = trg07Text(r, stream)
	}
	l.mu.Lock()
	l.samples = append(l.samples, s)
	l.mu.Unlock()
}

// trg07Reloader drives the reload sequence and records what it observed.
type trg07Reloader struct {
	t                     *testing.T
	tr                    *triage
	f                     *fake
	probe                 func() string
	validN, invalidN, gen int
	invalidApplied        int
	maxStarted            atomic.Int64
	problems              []string
}

func (rl *trg07Reloader) reload() response {
	return rl.tr.call(rl.t, http.MethodPost, rl.tr.admin+"/v1/admin/reload", nil)
}

func (rl *trg07Reloader) problem(format string, a ...any) {
	rl.problems = append(rl.problems, fmt.Sprintf(format, a...))
}

func (rl *trg07Reloader) valid() {
	rl.gen++
	marker := "gen-" + strconv.Itoa(rl.gen)
	rl.maxStarted.Store(int64(rl.gen))
	trg07Write(rl.t, rl.tr.cfgPath, trg07Config(rl.f, marker, ""))
	r := rl.reload()
	if r.status != http.StatusOK {
		rl.problem("valid reload %d: HTTP %d %s", rl.gen, r.status, r.body)
		return
	}
	rl.validN++
	if got := rl.probe(); got != marker {
		rl.problem("after valid reload %d probe saw %q", rl.gen, got)
	}
}

func (rl *trg07Reloader) invalid(i int) {
	kinds := []string{"unknown_target", "unknown_strategy", "syntax"}
	rl.invalidN++
	kind := kinds[(i/2)%len(kinds)]
	trg07Write(rl.t, rl.tr.cfgPath, trg07Config(rl.f, "inv-"+strconv.Itoa(i), kind))
	if r := rl.reload(); r.status < 400 {
		rl.invalidApplied++
		rl.problem("invalid reload %d (%s) accepted: HTTP %d", i, kind, r.status)
	}
	if got := rl.probe(); got != "gen-"+strconv.Itoa(rl.gen) {
		rl.invalidApplied++
		rl.problem("invalid reload %d changed behaviour: probe saw %q", i, got)
	}
	models := rl.tr.call(rl.t, http.MethodGet, rl.tr.data+"/v1/models", nil)
	if strings.Contains(string(models.body), trg07BadRoute) {
		rl.invalidApplied++
		rl.problem("invalid route listed after reload %d", i)
	}
}

// trg07Stats is the classification of the load responses.
type trg07Stats struct {
	router5xx, non2xx, markerInvalid, transportErrs, badText, aheadOfReload int
	byStatus                                                                map[int]int
	failures                                                                []string
}

func (st *trg07Stats) note(s string) {
	if len(st.failures) < 10 {
		st.failures = append(st.failures, s)
	}
}

// routerCaused reports whether a 5xx shows no upstream 5xx attempt.
func trg07RouterCaused(ctx context.Context, tr *triage, id string) bool {
	if id == "" {
		return true
	}
	d, _, err := tr.decision(ctx, id)
	if err != nil {
		return true
	}
	for _, a := range d.Attempts {
		if a.HTTPStatus >= 500 {
			return false
		}
	}
	return true
}

func (st *trg07Stats) add(ctx context.Context, tr *triage, s trg07Sample, maxStarted int64) {
	switch {
	case s.err != nil && s.status == 0:
		st.transportErrs++
		st.non2xx++
		st.note("transport: " + s.err.Error())
	case s.status >= 500:
		st.non2xx++
		st.byStatus[s.status]++
		if trg07RouterCaused(ctx, tr, s.decision) {
			st.router5xx++
			st.note(fmt.Sprintf("router 5xx: status %d decision %q", s.status, s.decision))
		}
	case s.status != http.StatusOK:
		st.non2xx++
		st.byStatus[s.status]++
		st.note(fmt.Sprintf("status %d", s.status))
	case s.err != nil:
		st.badText++
		st.note("bad body: " + s.err.Error())
	default:
		st.marker(s, maxStarted)
	}
}

func (st *trg07Stats) marker(s trg07Sample, maxStarted int64) {
	m := trg07Marker.FindStringSubmatch(s.text)
	switch {
	case m == nil:
		st.badText++
		st.note(fmt.Sprintf("no marker in %.60q", s.text))
	case m[1] == "inv":
		st.markerInvalid++
	default:
		if n, _ := strconv.Atoi(m[2]); int64(n) > maxStarted {
			st.aheadOfReload++
		}
	}
}

// trg07ReloadCount reads triage_config_reload_total{result}.
func trg07ReloadCount(t *testing.T, tr *triage, result string) float64 {
	t.Helper()
	for _, l := range strings.Split(tr.metrics(t), "\n") {
		if rest, ok := strings.CutPrefix(l, `triage_config_reload_total{result="`+result+`"} `); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			require.NoError(t, err)
			return v
		}
	}
	return 0
}

// trg07Verdict checks the invariants beyond the numeric thresholds: every
// response succeeded with a valid marker, every reload behaved as expected and
// the reload metric matches.
func trg07Verdict(rl *trg07Reloader, st *trg07Stats, load *trg07Load, invalidApplied int, rps, dOK, dErr float64) bool {
	return load.maxLag < 100*time.Millisecond && rps > 0 && invalidApplied == 0 &&
		len(rl.problems) == 0 && st.non2xx == 0 && st.badText == 0 && st.aheadOfReload == 0 &&
		rl.validN == trg07Reloads/2 && rl.invalidN == trg07Reloads/2 &&
		dOK == float64(rl.validN) && dErr == float64(rl.invalidN) && len(load.samples) == load.launched
}

// TestAcceptance_TRG07_HotReload implements TRG-07.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG07_HotReload(t *testing.T) {
	th := thresholds(t, "TRG-07")
	wantReloads, wantRPS := int(num(t, th, "reloads")), num(t, th, "rps")
	maxRouter5xx, maxInvalidApplied := num(t, th, "router_5xx"), num(t, th, "invalid_applied")

	f := composeFake(t)
	tr := startTriage(t, trg07Config(f, "gen-0", ""))
	ctx := context.Background()
	okBefore, errBefore := trg07ReloadCount(t, tr, "success"), trg07ReloadCount(t, tr, "error")

	probe := func() string {
		r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody(trg07Route, "probe", false))
		require.Equal(t, http.StatusOK, r.status, string(r.body))
		txt, err := trg07Text(r, false)
		require.NoError(t, err)
		return txt
	}
	require.Equal(t, "gen-0", probe())

	load := &trg07Load{tr: tr, stop: make(chan struct{}), done: make(chan struct{})}
	go load.run(ctx)

	// One reload per 500 ms: even index valid, odd index invalid.
	rl := &trg07Reloader{t: t, tr: tr, f: f, probe: probe}
	tick := time.NewTicker(trg07Every)
	defer tick.Stop()
	for i := range trg07Reloads {
		<-tick.C
		if i%2 == 0 {
			rl.valid()
		} else {
			rl.invalid(i)
		}
	}
	close(load.stop)
	<-load.done
	load.wg.Wait()

	st := &trg07Stats{byStatus: map[int]int{}}
	for _, s := range load.samples {
		st.add(ctx, tr, s, rl.maxStarted.Load())
	}
	invalidApplied := rl.invalidApplied + st.markerInvalid
	if m := tr.call(t, http.MethodGet, tr.data+"/v1/models", nil); strings.Contains(string(m.body), trg07BadRoute) {
		invalidApplied++
	}
	dOK := trg07ReloadCount(t, tr, "success") - okBefore
	dErr := trg07ReloadCount(t, tr, "error") - errBefore
	// Open-loop schedule: launches/(launches*interval), valid because maxLag bounds the pacing error.
	rps := float64(load.launched) / (time.Duration(load.launched) * (time.Second / trg07Rate)).Seconds()

	measured := map[string]any{
		"reloads": rl.validN + rl.invalidN, "valid": rl.validN, "invalid": rl.invalidN,
		"requests": load.launched, "responses": len(load.samples), "rps_achieved": rps,
		"router_5xx": st.router5xx, "invalid_applied": invalidApplied,
		"non_2xx": st.non2xx, "non_2xx_by_status": st.byStatus, "transport_errors": st.transportErrs,
		"bad_bodies": st.badText, "responses_with_invalid_marker": st.markerInvalid,
		"responses_ahead_of_reload": st.aheadOfReload, "load_seconds": load.loadDur.Seconds(),
		"max_launch_lag_ms":     float64(load.maxLag.Microseconds()) / 1000,
		"metric_reload_success": dOK, "metric_reload_error": dErr,
		"problems": rl.problems, "first_failures": st.failures,
	}
	pass := trg07Verdict(rl, st, load, invalidApplied, rps, dOK, dErr) &&
		rl.validN+rl.invalidN >= wantReloads && rps >= wantRPS &&
		float64(st.router5xx) <= maxRouter5xx && float64(invalidApplied) <= maxInvalidApplied
	details := fmt.Sprintf("%d reloads (%d valid, %d invalid) under %.1f RPS: %d router 5xx, %d invalid applied, "+
		"%d non-2xx", rl.validN+rl.invalidN, rl.validN, rl.invalidN, rps, st.router5xx, invalidApplied, st.non2xx)
	writeResult(t, Result{ID: "TRG-07", Measured: measured, Pass: pass, Details: details})
	t.Log(details)
	require.Empty(t, rl.problems)
	require.True(t, pass, "%s; metrics success=%v error=%v; failures=%v", details, dOK, dErr, st.failures)
}
