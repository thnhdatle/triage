// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/thnhdatle/triage/internal/testutil"
	"github.com/thnhdatle/triage/internal/wire"
)

func TestServe_HotReload_AppliesValidAndKeepsOldOnInvalid(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	base := baseConfig(f, defaultRoutes)
	tr := startTriage(t, base)
	write := func(content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(tr.cfgPath, []byte(content), 0o600))
	}
	reload := func() response { return tr.call(t, http.MethodPost, tr.admin+"/v1/admin/reload", nil) }

	write(base + `  - { name: added, pool: [local-small], privacy: { mode: off } }` + "\n")
	require.Equal(t, http.StatusOK, reload().status)
	assert.Contains(t, string(tr.call(t, http.MethodGet, tr.data+"/v1/models", nil).body), `"added"`)
	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("added", "hi", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))

	write(base + "bogus_key: 1\n")
	r = reload()
	assert.Equal(t, http.StatusUnprocessableEntity, r.status)
	cacheDir := t.TempDir()
	write(base + "  - { name: learned, pool: [local-small, cloud-strong], strategy: bert }\n" +
		"models: { cache_dir: " + cacheDir + " }\n")
	r = reload()
	assert.Equal(t, http.StatusUnprocessableEntity, r.status)
	assertBERTUnavailable(t, string(r.body)) // missing artifacts reject the reload (F6, F11)

	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("added", "still here", false))
	assert.Equal(t, http.StatusOK, r.status, "the previous configuration stays active")
	m := tr.metrics(t)
	assert.Contains(t, m, `triage_config_reload_total{result="success"} 1`)
	assert.Contains(t, m, `triage_config_reload_total{result="error"} 2`)
}

func TestServe_HotReload_RaceWithTraffic(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	base := baseConfig(f, defaultRoutes)
	reloads := make(chan struct{})
	tr := startTriage(t, base, func(o *wire.ServeOptions) { o.Reload = reloads })
	variants := []string{
		base,
		strings.Replace(base, "threshold: 0.5", "threshold: 0.9", 1),
		strings.Replace(base, "attempt_timeout: 2s", "attempt_timeout: 3s", 1),
		base + "this is: [not valid\n",
	}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var statuses []int
	for w := range 4 {
		wg.Go(func() {
			for i := 0; ctx.Err() == nil; i++ {
				r, err := tr.post(ctx, tr.data+"/v1/chat/completions",
					chatBody("triage-auto", "load "+string(rune('a'+w)), i%2 == 0))
				if err != nil {
					if ctx.Err() != nil {
						return // the test is over; the request was cut by the client
					}
					r.status = -1
				}
				mu.Lock()
				statuses = append(statuses, r.status)
				mu.Unlock()
			}
		})
	}
	for i := range 40 {
		require.NoError(t, os.WriteFile(tr.cfgPath, []byte(variants[i%len(variants)]), 0o600))
		if i%2 == 0 {
			reloads <- struct{}{} // SIGHUP path
		} else {
			tr.call(t, http.MethodPost, tr.admin+"/v1/admin/reload", nil)
		}
		testutil.Eventually(t, func() bool { return reloadCount(t, tr) == i+1 }, 10*time.Second)
	}
	assert.Contains(t, tr.metrics(t), `triage_config_reload_total{result="error"} 10`)
	cancel()
	wg.Wait()
	require.NotEmpty(t, statuses)
	for _, s := range statuses {
		require.Equal(t, http.StatusOK, s)
	}
}

func TestServe_GracefulShutdown_DrainsInFlight(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))
	f.script(t, `[{"model":"fake-small","scenario":"slow","count":1,"params":{"latency_ms":400}}]`)
	got := make(chan response, 1)
	go func() {
		r, err := tr.post(t.Context(), tr.data+"/v1/chat/completions", chatBody("triage-auto", "slow one", false))
		if err != nil {
			r.body = []byte(err.Error())
		}
		got <- r
	}()
	testutil.Eventually(t, func() bool { return len(f.requests(t)) > 0 }, 5*time.Second)
	tr.stop(t) // Serve returns nil only after draining
	r := <-got
	assert.Equal(t, http.StatusOK, r.status, string(r.body))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tr.data+"/healthz", nil)
	require.NoError(t, err)
	resp, err := tr.hc.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "listener closed after shutdown")
}

func TestServe_Tracing_SpanTreeAndPropagation(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "trace me", false),
		"traceparent", parent)
	require.Equal(t, http.StatusOK, r.status, string(r.body))

	spans := tr.exporter.GetSpans()
	root := findSpan(t, spans, "route request")
	attempt := findSpan(t, spans, "chat fake-small")
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", root.SpanContext.TraceID().String(), "continues W3C context")
	assert.Equal(t, root.SpanContext.SpanID(), attempt.Parent.SpanID())
	assert.Equal(t, trace.SpanKindServer, root.SpanKind)
	ra, aa := attrs(root.Attributes), attrs(attempt.Attributes)
	for _, k := range []string{
		"triage.route", "triage.strategy", "triage.score", "triage.tier", "triage.privacy.mode",
		"triage.privacy.findings", "gen_ai.operation.name", "gen_ai.request.model", "gen_ai.response.model",
	} {
		assert.Contains(t, ra, k)
	}
	assert.Equal(t, "triage-auto", ra["gen_ai.request.model"].AsString())
	assert.Equal(t, "fake-small", ra["gen_ai.response.model"].AsString())
	assert.Equal(t, "fake", aa["gen_ai.provider.name"].AsString())
	for _, k := range []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens"} {
		assert.Equal(t, aa[k].AsInt64(), ra[k].AsInt64(), "single attempt: usage equal on route request (C3)")
		assert.Positive(t, aa[k].AsInt64())
	}
	recs := f.requests(t)
	require.Len(t, recs, 1)
	assert.Contains(t, recs[0].Headers.Get("Traceparent"), "4bf92f3577b34da6a3ce929d0e0e4736", "outgoing traceparent")
}

// reloadCount sums triage_config_reload_total over results.
func reloadCount(t *testing.T, tr *triage) int {
	t.Helper()
	n := 0
	for _, l := range strings.Split(tr.metrics(t), "\n") {
		if rest, ok := strings.CutPrefix(l, "triage_config_reload_total{"); ok {
			v, err := strconv.Atoi(rest[strings.LastIndex(rest, " ")+1:])
			require.NoError(t, err)
			n += v
		}
	}
	return n
}

func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("span %q not found among %d spans", name, len(spans))
	return tracetest.SpanStub{}
}

func attrs(kv []attribute.KeyValue) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, a := range kv {
		m[string(a.Key)] = a.Value
	}
	return m
}
