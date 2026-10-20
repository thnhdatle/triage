// SPDX-License-Identifier: Apache-2.0

package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/adapter/httpapi"
	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeRouter is a scriptable httpapi.Router recording the last requests.
type fakeRouter struct {
	mu     sync.Mutex
	chat   func(req chat.Request) (route.Outcome, error)
	stream func(req chat.Request, sink dispatch.Sink) (route.Outcome, error)
	embed  func(in route.EmbeddingsRequest) (route.Outcome, error)
	reqs   []chat.Request
	embeds []route.EmbeddingsRequest
}

func (f *fakeRouter) Chat(_ context.Context, req chat.Request) (route.Outcome, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	return f.chat(req)
}

func (f *fakeRouter) ChatStream(_ context.Context, req chat.Request, sink dispatch.Sink) (route.Outcome, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	return f.stream(req, sink)
}

func (f *fakeRouter) Embeddings(_ context.Context, in route.EmbeddingsRequest) (route.Outcome, error) {
	f.mu.Lock()
	f.embeds = append(f.embeds, in)
	f.mu.Unlock()
	return f.embed(in)
}

func (f *fakeRouter) last(t *testing.T) chat.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.reqs)
	return f.reqs[len(f.reqs)-1]
}

type fakeCatalog struct {
	routes  []string
	targets []model.Target
}

func (c fakeCatalog) Routes() []string        { return c.routes }
func (c fakeCatalog) Targets() []model.Target { return c.targets }

// idGen returns id-1, id-2, ...
func idGen() func() string {
	var n atomic.Int64
	return func() string { return "id-" + strconv.FormatInt(n.Add(1), 10) }
}

func okOutcome() route.Outcome {
	return route.Outcome{
		DecisionID: "dec-1", Route: "auto", Target: "local-weak", Model: "qwen-up",
		Response: chat.Response{
			Model:   "ignored",
			Choices: []chat.Choice{{Message: chat.TextMessage(chat.RoleAssistant, "hello"), FinishReason: chat.FinishStop}},
			Usage:   chat.Usage{PromptTokens: 5, CompletionTokens: 2, CachedPromptTokens: 1},
		},
		Decision: decision.Decision{ID: "dec-1", Reasons: []string{"rule:length", "café"}},
	}
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{
		chat: func(chat.Request) (route.Outcome, error) { return okOutcome(), nil },
		embed: func(route.EmbeddingsRequest) (route.Outcome, error) {
			return route.Outcome{}, nil
		},
	}
}

func newServer(t *testing.T, r httpapi.Router, opts ...httpapi.Option) *httpapi.Server {
	t.Helper()
	cat := fakeCatalog{
		routes: []string{"strong", "auto"},
		targets: []model.Target{{
			Name: "local-weak", Provider: "lmstudio", Model: "qwen", Locality: model.LocalityLocal,
			Tier: model.TierWeak, ContextWindow: 8192, Capabilities: []model.Capability{model.CapTools},
		}},
	}
	s, err := httpapi.New(r, cat, idGen(), opts...)
	require.NoError(t, err)
	return s
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func errField(t *testing.T, rec *httptest.ResponseRecorder, field string) any {
	t.Helper()
	e, ok := decodeBody(t, rec)["error"].(map[string]any)
	require.True(t, ok, rec.Body.String())
	return e[field]
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(b)
}
