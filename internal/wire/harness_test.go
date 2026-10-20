// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/thnhdatle/triage/internal/fakellm"
	"github.com/thnhdatle/triage/internal/wire"
)

// fake is a fakellm (contract C2): in-process, or the container of the
// integration tests.
type fake struct {
	url string
	hc  *http.Client
}

func startFake(t *testing.T) *fake {
	t.Helper()
	f, err := fakellm.New(fakellm.WithRecord(true, fakellm.DefaultRecordLimit))
	require.NoError(t, err)
	srv := httptest.NewServer(f.Handler())
	tr := &http.Transport{}
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		srv.CloseClientConnections()
		srv.Close()
	})
	return &fake{url: srv.URL, hc: &http.Client{Transport: tr, Timeout: 10 * time.Second}}
}

func (f *fake) baseURL() string { return f.url + "/v1" }

type fakeRecord struct {
	Headers http.Header `json:"headers"`
	Body    string      `json:"body"`
	Model   string      `json:"model"`
	Kind    string      `json:"kind"`
}

func (f *fake) requests(t *testing.T) []fakeRecord {
	t.Helper()
	var out struct {
		Requests []fakeRecord `json:"requests"`
		Dropped  int          `json:"dropped"`
	}
	f.do(t, http.MethodGet, "/_fake/requests", nil, &out)
	require.Zero(t, out.Dropped)
	return out.Requests
}

func (f *fake) script(t *testing.T, rules string) {
	t.Helper()
	f.do(t, http.MethodPut, "/_fake/script", []byte(rules), nil)
}

func (f *fake) do(t *testing.T, method, path string, body []byte, out any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.url+path, bytes.NewReader(body))
	require.NoError(t, err)
	resp, err := f.hc.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Less(t, resp.StatusCode, 300, string(data))
	if out != nil {
		require.NoError(t, json.Unmarshal(data, out))
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

type serveOpt func(*wire.ServeOptions)

func startTriage(t *testing.T, cfgYAML string, opts ...serveOpt) *triage {
	t.Helper()
	path := filepath.Join(t.TempDir(), "triage.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfgYAML), 0o600))
	exp := tracetest.NewInMemoryExporter()
	addrs := make(chan [2]net.Addr, 1)
	o := wire.ServeOptions{
		ConfigPath: path, SpanExporter: exp, WatchInterval: -1, ShutdownTimeout: 5 * time.Second,
		Env:     wire.Env{Getenv: func(k string) string { return testEnv[k] }},
		Started: func(d, a net.Addr) { addrs <- [2]net.Addr{d, a} },
	}
	for _, fn := range opts {
		fn(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr := &triage{cancel: cancel, done: make(chan error, 1), exporter: exp, cfgPath: path}
	go func() { tr.done <- wire.Serve(ctx, o) }()
	select {
	case a := <-addrs:
		tr.data, tr.admin = "http://"+a[0].String(), "http://"+a[1].String()
	case err := <-tr.done:
		cancel()
		t.Fatalf("serve failed to start: %v", err)
	}
	transport := &http.Transport{}
	tr.hc = &http.Client{Transport: transport, Timeout: 20 * time.Second}
	t.Cleanup(func() { tr.stop(t) })
	return tr
}

// testEnv is the fake process environment of every Serve in the tests.
var testEnv = map[string]string{"TRIAGE_TEST_KEYS": "k1,k2", "FAKE_KEY": "sk-test"}

func (tr *triage) stop(t *testing.T) {
	t.Helper()
	// A connection the client dialled but never used (a request cancelled
	// right after the dial) counts as active for http.Server.Shutdown for
	// 5s; close idle client connections first so draining is immediate.
	if c, ok := tr.hc.Transport.(*http.Transport); ok {
		c.CloseIdleConnections()
	}
	tr.cancel()
	select {
	case err, ok := <-tr.done:
		if ok {
			if err != nil {
				buf := make([]byte, 1<<20)
				t.Log(string(buf[:runtime.Stack(buf, true)]))
			}
			require.NoError(t, err)
			close(tr.done)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop")
	}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (tr *triage) call(t *testing.T, method, url string, body any, headers ...string) response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rd)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := tr.hc.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

// post sends a JSON body without failing the test, for use off the test
// goroutine.
func (tr *triage) post(ctx context.Context, url string, body any) (response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return response{}, err
	}
	resp, err := tr.hc.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: data}, err
}

// assertBERTUnavailable checks the error names the fix: the pull command,
// or the build tag on a binary without ONNX Runtime.
func assertBERTUnavailable(t *testing.T, msg string) {
	t.Helper()
	if !strings.Contains(msg, "triage models pull bert") {
		assert.Contains(t, msg, "-tags noonnx")
	}
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
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(data, &v), string(data))
	return v
}

// sseEvents returns the data payloads of an SSE body.
func sseEvents(body []byte) []string {
	var data []string
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			data = append(data, d)
		}
	}
	return data
}

// streamText concatenates the delta contents of chunk payloads.
func streamText(t *testing.T, data []string) string {
	t.Helper()
	var b strings.Builder
	for _, d := range data {
		if d == "[DONE]" {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal([]byte(d), &c), d)
		for _, ch := range c.Choices {
			b.WriteString(ch.Delta.Content)
		}
	}
	return b.String()
}

type decisionView struct {
	ID       string         `json:"id"`
	Route    string         `json:"route"`
	Privacy  string         `json:"privacy"`
	Findings map[string]int `json:"findings"`
	Strategy string         `json:"strategy"`
	Tier     string         `json:"tier"`
	Reasons  []string       `json:"reasons"`
	Error    string         `json:"error"`
	Attempts []struct {
		Target       string `json:"target"`
		Locality     string `json:"locality"`
		Masked       bool   `json:"masked"`
		Continuation bool   `json:"continuation"`
		Status       string `json:"status"`
	} `json:"attempts"`
	CostUSD float64 `json:"cost_usd"`
}

func (tr *triage) decision(t *testing.T, id string) decisionView {
	t.Helper()
	r := tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions/"+id, nil)
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	return decode[decisionView](t, r.body)
}

func (tr *triage) metrics(t *testing.T) string {
	t.Helper()
	r := tr.call(t, http.MethodGet, tr.admin+"/metrics", nil)
	require.Equal(t, http.StatusOK, r.status)
	return string(r.body)
}

// baseConfig is a two-target configuration over fakellm: a local weak and a
// cloud strong target. extra is appended verbatim (routes, overrides).
func baseConfig(f *fake, extra string) string {
	return `
server: { listen: "127.0.0.1:0" }
admin: { listen: "127.0.0.1:0" }
auth: { enabled: false }
api: { expose_decision_detail: true, expose_targets: true }
providers:
  fake: { type: openaicompat, base_url: "` + f.baseURL() + `" }
health: { interval: 1h }
targets:
  - { name: local-small, provider: fake, model: fake-small, locality: local, tier: weak, context_window: 32768,
      capabilities: [tools, json_mode], pricing: { input_per_mtok: 0, output_per_mtok: 0 } }
  - { name: cloud-strong, provider: fake, model: fake-strong, locality: cloud, tier: strong, context_window: 200000,
      capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: echo },
      pricing: { input_per_mtok: 1.25, output_per_mtok: 10.0 } }
  - { name: local-embed, provider: fake, model: fake-embed, locality: local, capabilities: [embeddings] }
dispatch: { attempt_timeout: 2s, total_timeout: 10s, backoff: { base: 1ms, max: 5ms } }
` + extra
}

const defaultRoutes = `
routes:
  - name: triage-auto
    pool: [local-small, cloud-strong]
    privacy: { mode: mask }
    strategy: length
    threshold: 0.5
    fallback: [tier:strong, tier:weak]
  - name: triage-random
    pool: [local-small, cloud-strong]
    privacy: { mode: off }
    strategy: random
  - name: strong-masked
    pool: [local-small, cloud-strong]
    privacy: { mode: mask }
    default_tier: strong
  - name: private
    pool: [local-small, cloud-strong]
    privacy: { mode: local_only }
    default_tier: strong
  - name: embed
    pool: [local-embed]
    privacy: { mode: mask }
`
