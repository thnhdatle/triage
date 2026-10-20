// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/thnhdatle/triage/internal/wire"
)

// The acceptance tests run Triage in-process (wire.Serve with the real
// adapters) against the fakellm service of deploy/docker-compose.yml, which
// the runner brings up; every upstream request crosses the Docker network.

const (
	fakeURLVar     = "TRIAGE_ACCEPTANCE_FAKELLM_URL"
	defaultFakeURL = "http://localhost:8090"
)

// fake is the fakellm compose service (contract C2).
type fake struct {
	url string
	hc  *http.Client
}

// composeFake returns the fakellm service with its script and request log
// cleared, so the test sees only its own traffic.
func composeFake(tb testing.TB) *fake {
	tb.Helper()
	u := os.Getenv(fakeURLVar)
	if u == "" {
		u = defaultFakeURL
	}
	tr := &http.Transport{}
	tb.Cleanup(tr.CloseIdleConnections)
	f := &fake{url: strings.TrimSuffix(u, "/"), hc: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
	f.do(tb, http.MethodDelete, "/_fake/script", nil, nil)
	f.do(tb, http.MethodDelete, "/_fake/requests", nil, nil)
	return f
}

func (f *fake) baseURL() string { return f.url + "/v1" }

// fakeRecord is one entry of the fakellm request log.
type fakeRecord struct {
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Headers http.Header `json:"headers"`
	Body    string      `json:"body"`
	Model   string      `json:"model"`
	Kind    string      `json:"kind"`
}

// requests returns the request log and the number of evicted entries.
func (f *fake) requests(tb testing.TB) ([]fakeRecord, int) {
	tb.Helper()
	var out struct {
		Requests []fakeRecord `json:"requests"`
		Dropped  int          `json:"dropped"`
	}
	f.do(tb, http.MethodGet, "/_fake/requests", nil, &out)
	return out.Requests, out.Dropped
}

// script replaces the per-model script (PUT /_fake/script).
func (f *fake) script(tb testing.TB, rules any) {
	tb.Helper()
	b, err := json.Marshal(rules)
	require.NoError(tb, err)
	f.do(tb, http.MethodPut, "/_fake/script", b, nil)
}

func (f *fake) do(tb testing.TB, method, path string, body []byte, out any) {
	tb.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.url+path, bytes.NewReader(body))
	require.NoError(tb, err)
	resp, err := f.hc.Do(req)
	require.NoError(tb, err, "fakellm at %s (set %s)", f.url, fakeURLVar)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(tb, err)
	require.Less(tb, resp.StatusCode, 300, string(data))
	if out != nil {
		require.NoError(tb, json.Unmarshal(data, out))
	}
}

// triage is a Serve run in-process on loopback ports.
type triage struct {
	data, admin string
	hc          *http.Client
	cancel      context.CancelFunc
	done        chan error
	exporter    *tracetest.InMemoryExporter
	cfgPath     string
}

// startTriage writes cfgYAML to a temporary file and serves it. Listeners
// must be "127.0.0.1:0".
func startTriage(tb testing.TB, cfgYAML string) *triage {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "triage.yaml")
	require.NoError(tb, os.WriteFile(path, []byte(cfgYAML), 0o600))
	exp := tracetest.NewInMemoryExporter()
	addrs := make(chan [2]net.Addr, 1)
	o := wire.ServeOptions{
		ConfigPath: path, SpanExporter: exp, WatchInterval: -1, ShutdownTimeout: 10 * time.Second,
		Env:     wire.Env{Getenv: os.Getenv},
		Started: func(d, a net.Addr) { addrs <- [2]net.Addr{d, a} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr := &triage{cancel: cancel, done: make(chan error, 1), exporter: exp, cfgPath: path}
	go func() { tr.done <- wire.Serve(ctx, o) }()
	select {
	case a := <-addrs:
		tr.data, tr.admin = "http://"+a[0].String(), "http://"+a[1].String()
	case err := <-tr.done:
		cancel()
		tb.Fatalf("serve failed to start: %v", err)
	}
	tr.hc = &http.Client{
		Transport: &http.Transport{MaxIdleConnsPerHost: 256, MaxConnsPerHost: 0},
		Timeout:   60 * time.Second,
	}
	tb.Cleanup(func() { tr.stop(tb) })
	return tr
}

func (tr *triage) stop(tb testing.TB) {
	tb.Helper()
	if c, ok := tr.hc.Transport.(*http.Transport); ok {
		c.CloseIdleConnections()
	}
	tr.cancel()
	select {
	case err, ok := <-tr.done:
		if ok {
			if err != nil {
				buf := make([]byte, 1<<20)
				tb.Log(string(buf[:runtime.Stack(buf, true)]))
			}
			require.NoError(tb, err)
			close(tr.done)
		}
	case <-time.After(30 * time.Second):
		tb.Fatal("serve did not stop")
	}
}

// response is a fully read HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

// call sends a JSON request and fails the test on transport errors.
func (tr *triage) call(tb testing.TB, method, url string, body any) response {
	tb.Helper()
	r, err := tr.send(context.Background(), method, url, body)
	require.NoError(tb, err)
	return r
}

// send sends a JSON request without failing the test, for use off the test
// goroutine. headers are name/value pairs.
func (tr *triage) send(ctx context.Context, method, url string, body any, headers ...string) (response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := tr.hc.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: data}, err
}

// chat posts to /v1/chat/completions.
func (tr *triage) chat(ctx context.Context, body any, headers ...string) (response, error) {
	return tr.send(ctx, http.MethodPost, tr.data+"/v1/chat/completions", body, headers...)
}

func chatBody(model, text string, stream bool) map[string]any {
	return map[string]any{
		"model": model, "stream": stream,
		"messages": []map[string]any{{"role": "user", "content": text}},
	}
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// sseLines returns every non-empty line of an SSE body (data and comments).
func sseLines(body []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// sseEvents returns the data payloads of an SSE body.
func sseEvents(body []byte) []string {
	var data []string
	for _, l := range sseLines(body) {
		if d, ok := strings.CutPrefix(l, "data: "); ok {
			data = append(data, d)
		}
	}
	return data
}

// streamChunk is the subset of a chat.completion.chunk the tests read.
type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int `json:"index"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// streamResult is a parsed SSE chat stream.
type streamResult struct {
	text, args   string
	finishReason string
	done         bool
	errorEvent   bool
	comments     []string
}

// parseStream reassembles content, tool-call arguments, the last finish
// reason, [DONE], error events and SSE comments.
func parseStream(body []byte) (streamResult, error) {
	var (
		r          streamResult
		text, args strings.Builder
	)
	for _, l := range sseLines(body) {
		if c, ok := strings.CutPrefix(l, ":"); ok {
			r.comments = append(r.comments, strings.TrimSpace(c))
			continue
		}
		d, ok := strings.CutPrefix(l, "data: ")
		if !ok {
			continue
		}
		if d == "[DONE]" {
			r.done = true
			continue
		}
		var c streamChunk
		if err := json.Unmarshal([]byte(d), &c); err != nil {
			return r, fmt.Errorf("chunk %q: %w", d, err)
		}
		if c.Error != nil {
			r.errorEvent = true
		}
		for _, ch := range c.Choices {
			text.WriteString(ch.Delta.Content)
			for _, tc := range ch.Delta.ToolCalls {
				args.WriteString(tc.Function.Arguments)
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				r.finishReason = *ch.FinishReason
			}
		}
	}
	r.text, r.args = text.String(), args.String()
	return r, nil
}

// decisionView is the subset of the decision JSON the tests read.
type decisionView struct {
	ID       string         `json:"id"`
	Route    string         `json:"route"`
	Privacy  string         `json:"privacy"`
	Findings map[string]int `json:"findings"`
	Strategy string         `json:"strategy"`
	Score    *float64       `json:"score"`
	Tier     string         `json:"tier"`
	Reasons  []string       `json:"reasons"`
	Unmask   string         `json:"unmask"`
	Error    string         `json:"error"`
	Attempts []struct {
		Target       string `json:"target"`
		Locality     string `json:"locality"`
		Masked       bool   `json:"masked"`
		Continuation bool   `json:"continuation"`
		Status       string `json:"status"`
		HTTPStatus   int    `json:"http_status"`
	} `json:"attempts"`
	CostUSD float64 `json:"cost_usd"`
}

// decision fetches a decision from the admin API.
func (tr *triage) decision(ctx context.Context, id string) (decisionView, response, error) {
	r, err := tr.send(ctx, http.MethodGet, tr.admin+"/v1/routing-decisions/"+id, nil)
	if err != nil {
		return decisionView{}, r, err
	}
	if r.status != http.StatusOK {
		return decisionView{}, r, fmt.Errorf("decision %s: HTTP %d: %s", id, r.status, r.body)
	}
	var d decisionView
	err = json.Unmarshal(r.body, &d)
	return d, r, err
}

func (tr *triage) metrics(tb testing.TB) string {
	tb.Helper()
	r := tr.call(tb, http.MethodGet, tr.admin+"/metrics", nil)
	require.Equal(tb, http.StatusOK, r.status)
	return string(r.body)
}

// num reads a numeric threshold.
func num(tb testing.TB, vals map[string]any, key string) float64 {
	tb.Helper()
	switch v := vals[key].(type) {
	case int:
		return float64(v)
	case float64:
		return v
	default:
		tb.Fatalf("threshold %q: want a number, got %T (%v)", key, vals[key], vals[key])
		return 0
	}
}

// targetsYAML is the shared provider and target section: a local weak and a
// cloud strong target over fakellm, with the given upstream model prefix and
// per-target extra headers (YAML flow maps, "{}" for none).
func targetsYAML(f *fake, prefix, localHeaders, cloudHeaders string) string {
	return `
server: { listen: "127.0.0.1:0" }
admin: { listen: "127.0.0.1:0" }
auth: { enabled: false }
api: { expose_decision_detail: true, expose_targets: true }
providers:
  fake: { type: openaicompat, base_url: "` + f.baseURL() + `", max_idle_conns_per_host: 256 }
health: { interval: 1h }
decisions: { buffer_size: 20000 }
targets:
  - { name: local-small, provider: fake, model: ` + prefix + `-small, locality: local, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode], extra_headers: ` + localHeaders + `,
      pricing: { input_per_mtok: 0, output_per_mtok: 0 } }
  - { name: cloud-strong, provider: fake, model: ` + prefix + `-strong, locality: cloud, tier: strong,
      context_window: 200000, capabilities: [tools, json_mode], extra_headers: ` + cloudHeaders + `,
      pricing: { input_per_mtok: 1.25, output_per_mtok: 10.0 } }
`
}
