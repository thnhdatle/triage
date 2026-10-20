// SPDX-License-Identifier: Apache-2.0

package telemetry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

func newReq(method, path string) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), method, path, nil)
}

func setup(t *testing.T) (*telemetry.Telemetry, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tm, err := telemetry.Setup(context.Background(), telemetry.Options{
		Version: "v1", SpanExporter: exp, CaptureContent: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tm.Shutdown(context.Background()) })
	return tm, exp
}

func scrape(t *testing.T, tm *telemetry.Telemetry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tm.MetricsHandler().ServeHTTP(rec, newReq(http.MethodGet, "/metrics"))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

func TestObserverRecordsSpecMetrics(t *testing.T) {
	t.Parallel()
	tm, _ := setup(t)
	o := tm.Observer()
	o.Observe(route.Event{Kind: route.EventFindings, Route: "r", Counts: map[string]int{"email": 2, "phone": 0}})
	o.Observe(route.Event{Kind: route.EventOversize, Route: "r"})
	o.Observe(route.Event{Kind: route.EventTier, Route: "r", Tier: model.TierStrong})
	o.Observe(route.Event{Kind: route.EventTier, Route: "r", Tier: "weird"})
	o.Observe(route.Event{Kind: route.EventScoring, Strategy: "bert", Duration: 30 * time.Millisecond})
	o.Observe(route.Event{Kind: route.EventScoringRejected, Strategy: "bert", Reason: "timeout"})
	o.Observe(route.Event{Kind: route.EventScoringRejected, Strategy: "bert", Reason: "free text with a@b.c"})
	o.Observe(route.Event{Kind: route.EventMFExternalEmbedding, Route: "r"})
	o.Observe(route.Event{Kind: route.EventUnmaskFailure, Route: "r"})
	o.Observe(route.Event{Kind: route.EventRouteOverhead, Route: "r", Duration: time.Millisecond})
	o.Observe(route.Event{Kind: "unknown"})
	tm.ObserveScore("bert", 0.4)
	tm.SetScoringQueueDepth("bert", 3)
	tm.ObserveUpstream("openai", "gpt", "200", time.Second)
	tm.ObserveUpstream("openai", "gpt", "", time.Second)
	tm.ObserveUpstream("openai", "gpt", "a very long free text status", time.Second)
	tm.ObserveFallback("r", "upstream_timeout")
	tm.AddTargetInflight("t1", 1)
	tm.SetTargetHealth("t1", true)
	tm.SetTargetHealth("t2", false)
	tm.ObserveConfigReload("success")
	tm.ObserveConfigReload("bogus")
	tm.ObserveConfigReload("error")
	tm.ObserveConfigReload("failure")
	tm.ObserveCost("r", "t1", 0.25)
	tm.ObserveCost("r", "t1", 0)
	tm.ObserveCost("r", "t2", -1)

	out := scrape(t, tm)
	for _, want := range []string{
		`triage_privacy_findings_total{route="r",type="email"} 2`,
		`triage_privacy_oversize_total{route="r"} 1`,
		`triage_tier_decisions_total{route="r",tier="strong"} 1`,
		`triage_tier_decisions_total{route="r",tier="other"} 1`,
		`triage_scoring_duration_seconds_count{strategy="bert"} 1`,
		`triage_scoring_rejected_total{reason="timeout",strategy="bert"} 1`,
		`triage_scoring_rejected_total{reason="other",strategy="bert"} 1`,
		`triage_mf_external_embeddings_total{route="r"} 1`,
		`triage_unmask_failures_total{route="r"} 1`,
		`triage_route_overhead_seconds_count{route="r"} 1`,
		`triage_strategy_score_count{strategy="bert"} 1`,
		`triage_scoring_queue_depth{strategy="bert"} 3`,
		`triage_upstream_request_duration_seconds_count{model="gpt",provider="openai",status="200"} 1`,
		`status="unknown"`,
		`status="other"`,
		`triage_fallback_attempts_total{reason="upstream_timeout",route="r"} 1`,
		`triage_target_inflight{target="t1"} 1`,
		`triage_target_healthy{target="t1"} 1`,
		`triage_target_healthy{target="t2"} 0`,
		`triage_config_reload_total{result="success"} 1`,
		`triage_config_reload_total{result="other"} 1`,
		`triage_config_reload_total{result="error"} 2`,
		`triage_cost_usd_total{route="r",target="t1"} 0.25`,
		`triage_build_info{commit="",go_version=`,
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "a@b.c")
	assert.NotContains(t, out, `type="phone"`)
	assert.NotContains(t, out, `target="t2"} -`, "non-positive costs are ignored")
}

func TestLabelCardinalityCapped(t *testing.T) {
	t.Parallel()
	tm, _ := setup(t)
	for i := range 80 {
		tm.Observer().Observe(route.Event{Kind: route.EventOversize, Route: fmt.Sprintf("r%d", i)})
	}
	tm.Observer().Observe(route.Event{Kind: route.EventOversize, Route: "r0"})
	out := scrape(t, tm)
	assert.Equal(t, 51, strings.Count(out, "triage_privacy_oversize_total{"))
	assert.Contains(t, out, `triage_privacy_oversize_total{route="other"} 30`)
	assert.Contains(t, out, `triage_privacy_oversize_total{route="r0"} 2`)
}

func TestMiddlewareMetricsAndSpan(t *testing.T) {
	t.Parallel()
	tm, exp := setup(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		telemetrySpanAttr(r)
		w.WriteHeader(http.StatusCreated)
		w.WriteHeader(http.StatusTeapot) // ignored for the status label
		w.(http.Flusher).Flush()
	})
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "x", http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusProcessing) })
	h := tm.Middleware()(mux)

	req := newReq(http.MethodPost, "/v1/chat/completions")
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	h.ServeHTTP(httptest.NewRecorder(), req)
	h.ServeHTTP(httptest.NewRecorder(), newReq(http.MethodGet, "/boom"))
	h.ServeHTTP(httptest.NewRecorder(), newReq(http.MethodGet, "/healthz"))
	h.ServeHTTP(httptest.NewRecorder(), newReq("BREW", "/nope"))
	h.ServeHTTP(httptest.NewRecorder(), newReq(http.MethodGet, "/info"))

	spans := exp.GetSpans()
	require.Len(t, spans, 4, "no span for /healthz")
	s := spans[0]
	assert.Equal(t, telemetry.RouteSpanName, s.Name)
	assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", s.SpanContext.TraceID().String())
	assert.Equal(t, "b7ad6b7169203331", s.Parent.SpanID().String())
	attrs := map[string]attribute.Value{}
	for _, a := range s.Attributes {
		attrs[string(a.Key)] = a.Value
	}
	assert.Equal(t, "POST /v1/chat/completions", attrs["http.route"].AsString())
	assert.Equal(t, int64(201), attrs["http.response.status_code"].AsInt64())
	assert.Equal(t, "bert", attrs["triage.route.strategy"].AsString())
	assert.Equal(t, "Error", spans[1].Status.Code.String())
	assert.Equal(t, "other", mustAttr(spans[2], "http.request.method").AsString())
	assert.Equal(t, "unmatched", mustAttr(spans[2], "http.route").AsString())

	out := scrape(t, tm)
	assert.Contains(t, out, `triage_http_requests_total{method="POST",path="POST /v1/chat/completions",status="201"} 1`)
	assert.Contains(t, out, `triage_http_requests_total{method="GET",path="GET /boom",status="500"} 1`)
	assert.Contains(t, out, `triage_http_requests_total{method="GET",path="GET /healthz",status="200"} 1`)
	assert.Contains(t, out, `path="unmatched",status="404"`)
	assert.Contains(t, out, `triage_http_request_duration_seconds_count{method="POST",path="POST /v1/chat/completions"} 1`)
	assert.NotNil(t, tm.Gatherer())
}

func mustAttr(s tracetest.SpanStub, key string) attribute.Value {
	for _, a := range s.Attributes {
		if string(a.Key) == key {
			return a.Value
		}
	}
	return attribute.Value{}
}

func telemetrySpanAttr(r *http.Request) {
	trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("triage.route.strategy", "bert"))
}

func TestStartAttemptChildSpan(t *testing.T) {
	t.Parallel()
	tm, exp := setup(t)
	ctx, parent := tm.Tracer().Start(context.Background(), telemetry.RouteSpanName)
	_, child := tm.StartAttempt(ctx, "chat", "openai", "gpt-x")
	child.End()
	parent.End()
	spans := exp.GetSpans()
	require.Len(t, spans, 2)
	assert.Equal(t, "chat gpt-x", spans[0].Name)
	assert.Equal(t, spans[1].SpanContext.SpanID(), spans[0].Parent.SpanID())
	assert.Equal(t, "openai", mustAttr(spans[0], "gen_ai.provider.name").AsString())
	assert.Equal(t, "chat", mustAttr(spans[0], "gen_ai.operation.name").AsString())
	assert.Equal(t, "gpt-x", mustAttr(spans[0], "gen_ai.request.model").AsString())
	assert.True(t, tm.CaptureContent())
}

func TestSetupDisabledIsNoop(t *testing.T) {
	t.Parallel()
	tm, err := telemetry.Setup(context.Background(), telemetry.Options{})
	require.NoError(t, err)
	assert.False(t, tm.CaptureContent())
	h := tm.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodGet, "/x"))
	assert.Equal(t, "ok", rec.Body.String())
	require.NoError(t, tm.Shutdown(context.Background()))
}

func TestSetupOTLPExporters(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	for _, p := range []string{"", "http/protobuf", "grpc"} {
		tm, err := telemetry.Setup(context.Background(), telemetry.Options{
			TracingEnabled: true, Protocol: p, ServiceName: "x",
		})
		require.NoError(t, err, p)
		require.NoError(t, tm.Shutdown(context.Background()), p)
	}
	_, err := telemetry.Setup(context.Background(), telemetry.Options{TracingEnabled: true, Protocol: "carrier-pigeon"})
	require.ErrorContains(t, err, "unknown otlp protocol")
}

func TestShutdownError(t *testing.T) {
	t.Parallel()
	tm, err := telemetry.Setup(context.Background(), telemetry.Options{SpanExporter: failingExporter{}})
	require.NoError(t, err)
	require.ErrorContains(t, tm.Shutdown(context.Background()), "boom")
}

type failingExporter struct{}

func (failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (failingExporter) Shutdown(context.Context) error                             { return errors.New("boom") }
