// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	pinned "github.com/thnhdatle/triage/models"
)

const (
	trg09Workers = 16
	trg09Schema  = "../api/schema/decision.v1.json"
	trg09PII     = "my email is jo.doe@example.com and card 4111 1111 1111 1111, thanks"
)

// trg09Routes is the route mix: strategies random/length/bert on the fixture,
// privacy off/mask/local_only, a tier-only route (no score) and two routes
// whose weak target always fails (500 / 429) so the request falls back.
const trg09Routes = `
dispatch: { attempt_timeout: 5s, total_timeout: 20s, backoff: { base: 1ms, max: 5ms } }
selection: { tokenizer: bert }
difficulty: { bert: { intra_op_threads: 1, max_concurrency: 4 } }
routes:
  - { name: rnd-off, pool: [local-small, cloud-strong], privacy: { mode: off }, strategy: random, threshold: 0.5 }
  - { name: len-mask, pool: [local-small, cloud-strong], privacy: { mode: mask }, strategy: length, threshold: 0.5,
      fallback: ["tier:strong", "tier:weak"] }
  - { name: len-local, pool: [local-small, cloud-strong], privacy: { mode: local_only },
      strategy: length, threshold: 0.5 }
  - { name: bert-off, pool: [local-small, cloud-strong], privacy: { mode: off }, strategy: bert, threshold: 0.5 }
  - { name: bert-mask, pool: [local-small, cloud-strong], privacy: { mode: mask }, strategy: bert, threshold: 0.5 }
  - { name: bert-local, pool: [local-small, cloud-strong], privacy: { mode: local_only },
      strategy: bert, threshold: 0.5 }
  - { name: tier-mask, pool: [local-small, cloud-strong], privacy: { mode: mask }, default_tier: strong }
  - { name: fb-500, pool: [flaky-weak, cloud-strong], privacy: { mode: mask },
      default_tier: weak, fallback: ["tier:strong"] }
  - { name: fb-429, pool: [limited-weak, cloud-strong], privacy: { mode: off },
      default_tier: weak, fallback: ["tier:strong"] }
`

var trg09RouteNames = []string{
	"rnd-off", "len-mask", "len-local", "bert-off", "bert-mask", "bert-local", "tier-mask", "fb-500", "fb-429",
}

// trg09Scored are the routes with a strategy, whose decisions carry a score.
var trg09Scored = map[string]bool{
	"rnd-off": true, "len-mask": true, "len-local": true, "bert-off": true, "bert-mask": true, "bert-local": true,
}

// trg09Bert lays out models.cache_dir with the bert-tiny fixture and the
// tokenizer cached by make bootstrap; a missing tokenizer fails the test.
func trg09Bert(tb testing.TB) string {
	tb.Helper()
	dir := os.Getenv("TRIAGE_TOKENIZER_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		require.NoError(tb, err)
		dir = filepath.Join(home, ".cache", "rayld", "triage", "tokenizers")
	}
	tok := filepath.Join(dir, "bert", "tokenizer.json")
	if _, err := os.Stat(tok); errors.Is(err, fs.ErrNotExist) {
		require.FailNowf(tb, "tokenizer file missing", "%s not found: run `make bootstrap`", tok)
	}
	m, err := pinned.Manifest()
	require.NoError(tb, err)
	e, _ := m.Entry("bert")
	cache := tb.TempDir()
	dst := filepath.Join(cache, e.Name, e.Revision)
	require.NoError(tb, os.MkdirAll(dst, 0o750))
	require.NoError(tb, os.Symlink(tok, filepath.Join(dst, "tokenizer.json")))
	for _, name := range []string{"graph.onnx", "model.safetensors", "model.int8.onnx"} {
		b, err := os.ReadFile(filepath.Join("..", "testdata", "models", "bert-tiny", name))
		require.NoError(tb, err)
		require.NoError(tb, os.WriteFile(filepath.Join(dst, name), b, 0o600))
	}
	return cache
}

func trg09Config(tb testing.TB, f *fake) string {
	tb.Helper()
	return targetsYAML(f, "trg09", "{}", "{ X-Fake-Scenario: echo }") + `
  - { name: flaky-weak, provider: fake, model: trg09-flaky, locality: local, tier: weak, context_window: 32768,
      capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: "500" },
      pricing: { input_per_mtok: 0, output_per_mtok: 0 } }
  - { name: limited-weak, provider: fake, model: trg09-limited, locality: local, tier: weak, context_window: 32768,
      capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: "429" },
      pricing: { input_per_mtok: 0, output_per_mtok: 0 } }
models: { cache_dir: ` + trg09Bert(tb) + ` }
` + trg09Routes
}

// trg09Prompt varies length and PII per request; the marker is unique.
func trg09Prompt(i int) string {
	p := fmt.Sprintf("req-%d explain the routing of this request", i)
	if i%5 == 0 {
		p += " " + trg09PII
	}
	// local_only routes refuse a strong-tier pick (503 by design), so they
	// never get the long prompt that makes the length strategy pick strong.
	if i%7 == 0 && !strings.HasSuffix(trg09RouteNames[i%len(trg09RouteNames)], "-local") {
		p += " " + strings.Repeat("lorem ipsum dolor sit amet ", 600)
	}
	return p
}

func trg09TraceID(i int) string { return fmt.Sprintf("%032x", uint64(0x7a11)<<40|uint64(i+1)) }

func trg09Attrs(kv []attribute.KeyValue) map[string]attribute.Value {
	m := make(map[string]attribute.Value, len(kv))
	for _, a := range kv {
		m[string(a.Key)] = a.Value
	}
	return m
}

// trg09Send sends request i with its own trace id.
func trg09Send(tr *triage, i int) (response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return tr.chat(ctx, chatBody(trg09RouteNames[i%len(trg09RouteNames)], trg09Prompt(i), i%3 == 0),
		"traceparent", fmt.Sprintf("00-%s-%016x-01", trg09TraceID(i), uint64(i+1)))
}

// trg09Response checks status, decision headers and the stream of one response.
func trg09Response(route string, stream bool, r response) string {
	if r.status != http.StatusOK {
		return fmt.Sprintf("status %d: %.200s", r.status, r.body)
	}
	for _, h := range []string{"X-Triage-Decision", "X-Triage-Route", "X-Triage-Target"} {
		if r.header.Get(h) == "" {
			return "missing header " + h
		}
	}
	if r.header.Get("X-Triage-Route") != route {
		return "X-Triage-Route " + r.header.Get("X-Triage-Route")
	}
	if stream {
		for _, e := range sseEvents(r.body) {
			if strings.Contains(e, `"error"`) {
				return "stream error event " + e
			}
		}
	}
	return ""
}

// trg09Check returns the first violated check of request i, or "".
func trg09Check(
	tr *triage, sch *jsonschema.Schema, i int, r response, spans tracetest.SpanStubs, upstream []fakeRecord,
) string {
	route := trg09RouteNames[i%len(trg09RouteNames)]
	stream := i%3 == 0
	tid := trg09TraceID(i)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if msg := trg09Response(route, stream, r); msg != "" {
		return msg
	}
	id := r.header.Get("X-Triage-Decision")
	d, raw, err := tr.decision(ctx, id)
	if err != nil {
		return "admin decision: " + err.Error()
	}
	if d.ID != id || d.Route != route {
		return "decision id/route mismatch"
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw.body)))
	if err != nil {
		return "decision json: " + err.Error()
	}
	if err := sch.Validate(inst); err != nil {
		return "decision schema: " + err.Error()
	}
	if msg := trg09Spans(d, spans, tid, route); msg != "" {
		return msg
	}
	if len(upstream) < len(d.Attempts) || len(upstream) == 0 {
		return fmt.Sprintf("outgoing traceparent seen on %d upstream requests, %d attempts", len(upstream), len(d.Attempts))
	}
	return ""
}

// trg09Trace picks the route request span and the chat spans of one trace.
func trg09Trace(spans tracetest.SpanStubs, tid string) (*tracetest.SpanStub, []tracetest.SpanStub, string) {
	var root *tracetest.SpanStub
	var children []tracetest.SpanStub
	for _, s := range spans {
		if s.SpanContext.TraceID().String() != tid {
			continue
		}
		switch {
		case s.Name == "route request":
			if root != nil {
				return nil, nil, "more than one route request span"
			}
			root = &s
		case strings.HasPrefix(s.Name, "chat "):
			children = append(children, s)
		}
	}
	if root == nil {
		return nil, nil, "no route request span"
	}
	return root, children, ""
}

// trg09Spans checks the span tree of one trace.
func trg09Spans(d decisionView, spans tracetest.SpanStubs, tid, route string) string {
	root, children, msg := trg09Trace(spans, tid)
	if msg != "" {
		return msg
	}
	ra := trg09Attrs(root.Attributes)
	for _, k := range []string{
		"triage.route", "triage.privacy.mode", "triage.privacy.findings", "gen_ai.operation.name",
		"gen_ai.request.model", "gen_ai.response.model", "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
	} {
		if _, ok := ra[k]; !ok {
			return "route request lacks attribute " + k
		}
	}
	if ra["triage.route"].AsString() != route || ra["gen_ai.request.model"].AsString() != route ||
		ra["gen_ai.operation.name"].AsString() != "chat" {
		return "route request route/gen_ai attributes wrong"
	}
	if d.Strategy != "" || d.Score != nil {
		if _, ok := ra["triage.score"]; !ok || ra["triage.strategy"].AsString() != d.Strategy {
			return "route request lacks score/strategy"
		}
	}
	if d.Tier != "" && ra["triage.tier"].AsString() != d.Tier {
		return "route request tier mismatch"
	}
	if trg09Scored[route] && d.Score == nil {
		return "strategy route without score"
	}
	return trg09Attempts(d, root, children, ra)
}

// trg09Attempts checks the per-attempt child spans against the decision.
func trg09Attempts(
	d decisionView, root *tracetest.SpanStub, children []tracetest.SpanStub, ra map[string]attribute.Value,
) string {
	if len(children) != len(d.Attempts) {
		return fmt.Sprintf("%d chat spans for %d attempts", len(children), len(d.Attempts))
	}
	for _, c := range children {
		if c.Parent.SpanID() != root.SpanContext.SpanID() {
			return "chat span is not a child of route request"
		}
		if _, ok := trg09Attrs(c.Attributes)["gen_ai.provider.name"]; !ok {
			return "chat span lacks gen_ai.provider.name"
		}
	}
	if len(d.Attempts) == 1 && d.Attempts[0].Status == "ok" {
		ca := trg09Attrs(children[0].Attributes)
		for _, k := range []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens"} {
			cv, ok := ca[k]
			if !ok || cv.AsInt64() != ra[k].AsInt64() || cv.AsInt64() <= 0 {
				return "usage mismatch between route request and chat span on " + k
			}
		}
	}
	return ""
}

// TestAcceptance_TRG09_Decisions implements TRG-09.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG09_Decisions(t *testing.T) {
	th := thresholds(t, "TRG-09")
	total := int(num(t, th, "requests"))
	want := num(t, th, "covered_ratio")

	sch, err := jsonschema.NewCompiler().Compile(trg09Schema)
	require.NoError(t, err)
	f := composeFake(t)
	tr := startTriage(t, trg09Config(t, f))

	resps := make([]response, total)
	errs := make([]error, total)
	sem := make(chan struct{}, trg09Workers)
	var wg sync.WaitGroup
	for i := range total {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			resps[i], errs[i] = trg09Send(tr, i)
		}()
	}
	wg.Wait()

	spans := trg09WaitSpans(tr.exporter, total)
	recs, dropped := f.requests(t)
	byTrace := map[string][]fakeRecord{}
	for _, rec := range recs {
		tp := rec.Headers.Get("Traceparent")
		if parts := strings.Split(tp, "-"); len(parts) == 4 {
			byTrace[parts[1]] = append(byTrace[parts[1]], rec)
		}
	}
	require.Zero(t, dropped, "fakellm request log overflowed")

	ok := 0
	var failures []string
	for i := range total {
		msg := ""
		if errs[i] != nil {
			msg = "request: " + errs[i].Error()
		} else {
			msg = trg09Check(tr, sch, i, resps[i], spans, byTrace[trg09TraceID(i)])
		}
		if msg == "" {
			ok++
		} else if len(failures) < 10 {
			failures = append(failures, fmt.Sprintf("request %d (%s): %s", i, trg09RouteNames[i%len(trg09RouteNames)], msg))
		}
	}
	ratio := float64(ok) / float64(total)
	pass := ratio >= want
	sort.Strings(failures)
	details := fmt.Sprintf("%d/%d requests satisfy all decision, schema, span and traceparent checks", ok, total)
	if len(failures) > 0 {
		details += "; first failures: " + strings.Join(failures, " | ")
	}
	writeResult(t, Result{
		ID: "TRG-09", Pass: pass, Details: details,
		Measured: map[string]any{"requests": total, "covered_ratio": ratio, "covered": ok},
	})
	require.True(t, pass, details)
}

// trg09WaitSpans returns the exported spans once every request has its
// `route request` span (the in-memory exporter may lag the response).
func trg09WaitSpans(exp *tracetest.InMemoryExporter, total int) tracetest.SpanStubs {
	deadline := time.Now().Add(30 * time.Second)
	for {
		spans := exp.GetSpans()
		n := 0
		for _, s := range spans {
			if s.Name == "route request" {
				n++
			}
		}
		if n >= total || time.Now().After(deadline) {
			return spans
		}
		time.Sleep(50 * time.Millisecond)
	}
}
