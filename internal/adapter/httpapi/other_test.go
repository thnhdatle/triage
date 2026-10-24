// SPDX-License-Identifier: Apache-2.0

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/httpapi"
	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

func embedOutcome() route.Outcome {
	return route.Outcome{
		DecisionID: "dec-e", Route: "emb", Target: "cloud-embed",
		Embeddings: provider.EmbedResponse{
			Model: "te3", Vectors: [][]float32{{0.5, -1}, {2, 0.25}}, Usage: chat.Usage{PromptTokens: 4},
		},
	}
}

func TestEmbeddings_Float_Success(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.embed = func(route.EmbeddingsRequest) (route.Outcome, error) { return embedOutcome(), nil }
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/embeddings",
		`{"model":"emb","input":["a","b"],"dimensions":2,"user":"u","encoding_format":"float"}`, "X-Fake-Scenario", "ok")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "dec-e", rec.Header().Get("X-Triage-Decision"))
	assert.JSONEq(t, `{"object":"list","model":"te3","usage":{"prompt_tokens":4,"total_tokens":4},"data":[
	  {"object":"embedding","index":0,"embedding":[0.5,-1]},
	  {"object":"embedding","index":1,"embedding":[2,0.25]}]}`, rec.Body.String())
	in := fr.embeds[0]
	assert.Equal(t, provider.EmbedRequest{
		Model: "emb", Input: []string{"a", "b"}, Dimensions: 2, User: "u", EncodingFormat: "float",
	}, in.Request)
	assert.Equal(t, map[string]string{"X-Fake-Scenario": "ok"}, in.Headers)
	assert.Equal(t, "id-2", in.ID)
}

func TestEmbeddings_Base64_AndSingleInput(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.embed = func(route.EmbeddingsRequest) (route.Outcome, error) { return embedOutcome(), nil }
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/embeddings",
		`{"model":"emb","input":"one","encoding_format":"base64"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"one"}, fr.embeds[0].Request.Input)
	data := decodeBody(t, rec)["data"].([]any)
	raw, err := base64.StdEncoding.DecodeString(data[0].(map[string]any)["embedding"].(string))
	require.NoError(t, err)
	require.Len(t, raw, 8)
	assert.InDelta(t, 0.5, math.Float32frombits(binary.LittleEndian.Uint32(raw)), 0)
	assert.InDelta(t, -1, math.Float32frombits(binary.LittleEndian.Uint32(raw[4:])), 0)
}

func TestEmbeddings_InvalidRequests_Table(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, body, param string }{
		{"bad json", `{`, ""},
		{"no model", `{"input":"x"}`, "model"},
		{"no input", `{"model":"emb"}`, "input"},
		{"empty input", `{"model":"emb","input":[]}`, "input"},
		{"token input", `{"model":"emb","input":[[1,2]]}`, "input"},
		{"negative dimensions", `{"model":"emb","input":"x","dimensions":-1}`, "dimensions"},
		{"bad format", `{"model":"emb","input":"x","encoding_format":"hex"}`, "encoding_format"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, newServer(t, newFakeRouter()).API(), http.MethodPost, "/v1/embeddings", tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			if tc.param != "" {
				assert.Equal(t, tc.param, errField(t, rec, "param"))
			}
		})
	}
}

func TestEmbeddings_RouterError_Mapped(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.embed = func(route.EmbeddingsRequest) (route.Outcome, error) {
		return route.Outcome{DecisionID: "dec-x"}, route.ErrNoEligibleTarget
	}
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/embeddings", `{"model":"emb","input":"x"}`)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "no_eligible_target", errField(t, rec, "code"))
	assert.Equal(t, "dec-x", rec.Header().Get("X-Triage-Decision"))
}

func TestModels_ListsRoutesSorted_TargetsOnlyWhenExposed(t *testing.T) {
	t.Parallel()
	ids := func(rec *httptest.ResponseRecorder) []string {
		data := decodeBody(t, rec)["data"].([]any)
		out := make([]string, 0, len(data))
		for _, m := range data {
			out = append(out, m.(map[string]any)["id"].(string))
		}
		return out
	}
	rec := do(t, newServer(t, newFakeRouter()).API(), http.MethodGet, "/v1/models", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "list", decodeBody(t, rec)["object"])
	assert.Equal(t, []string{"auto", "strong"}, ids(rec))
	first := decodeBody(t, rec)["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "model", first["object"])
	assert.Equal(t, "triage", first["owned_by"])

	exposed := newServer(t, newFakeRouter(), httpapi.WithSettings(func() httpapi.Settings {
		return httpapi.Settings{ExposeTargets: true}
	})).API()
	rec = do(t, exposed, http.MethodGet, "/v1/models", "")
	assert.Equal(t, []string{"auto", "strong", "local-weak"}, ids(rec))
}

func TestAuth_Table(t *testing.T) {
	t.Parallel()
	h := newServer(t, newFakeRouter(), httpapi.WithAuth(httpapi.NewKeyAuth([]string{"k1", "", "k2"}))).API()
	tests := []struct {
		name   string
		header []string
		status int
	}{
		{"no header", nil, 401},
		{"wrong scheme", []string{"Authorization", "Basic k1"}, 401},
		{"wrong key", []string{"Authorization", "Bearer nope"}, 401},
		{"empty key", []string{"Authorization", "Bearer "}, 401},
		{"first key", []string{"Authorization", "Bearer k1"}, 200},
		{"second key", []string{"Authorization", "Bearer  k2"}, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, h, http.MethodGet, "/v1/models", "", tc.header...)

			require.Equal(t, tc.status, rec.Code)
			if tc.status == 401 {
				assert.Equal(t, "invalid_api_key", errField(t, rec, "code"))
				assert.Equal(t, "authentication_error", errField(t, rec, "type"))
				assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
	assert.Equal(t, 200, do(t, h, http.MethodGet, "/healthz", "").Code, "probes need no key")
	assert.Equal(t, 401, do(t, h, http.MethodPost, "/v1/chat/completions", `{}`).Code)
	assert.Equal(t, 401, do(t, h, http.MethodPost, "/v1/embeddings", `{}`).Code)
}

func TestRouting_MethodAndPath(t *testing.T) {
	t.Parallel()
	h := newServer(t, newFakeRouter()).API()

	rec := do(t, h, http.MethodGet, "/v1/chat/completions", "")
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "POST", rec.Header().Get("Allow"))
	assert.Equal(t, "method_not_allowed", errField(t, rec, "code"))

	rec = do(t, h, http.MethodGet, "/nope", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not_found", errField(t, rec, "code"))
}

func TestRequestID_AcceptedOrGenerated(t *testing.T) {
	t.Parallel()
	h := newServer(t, newFakeRouter()).API()

	got := func(id string) string {
		return do(t, h, http.MethodGet, "/healthz", "", "X-Request-Id", id).Header().Get("X-Request-Id")
	}
	assert.Equal(t, "abc-123", got("abc-123"))
	assert.Equal(t, "id-1", got("bad id!"))
}

func TestRecover_PanicBecomes500(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.chat = func(chat.Request) (route.Outcome, error) { panic("boom") }
	var logs bytes.Buffer
	h := newServer(t, fr, httpapi.WithLogger(slog.New(slog.NewTextHandler(&logs, nil)))).API()

	rec := do(t, h, http.MethodPost, "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"x"}]}`)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal_error", errField(t, rec, "code"))
	assert.Equal(t, "internal error", errField(t, rec, "message"))
	assert.Contains(t, logs.String(), "boom")
}

func TestRecover_AbortHandlerRepanics(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.chat = func(chat.Request) (route.Outcome, error) { panic(http.ErrAbortHandler) }
	h := newServer(t, fr).API()

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		do(t, h, http.MethodPost, "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"x"}]}`)
	})
}

func TestAccessLog_AndMiddleware(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	var seen []string
	mw := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := newServer(t, newFakeRouter(),
		httpapi.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))), httpapi.WithAccessLog(true),
		httpapi.WithMiddleware(mw("outer"), mw("inner")), httpapi.WithLogger(nil)).API()

	do(t, h, http.MethodPost, "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"x"}]}`)
	do(t, h, http.MethodGet, "/v1/models", "")

	assert.Equal(t, []string{"outer", "inner", "outer", "inner"}, seen)
	assert.Equal(t, 2, strings.Count(logs.String(), "msg=request"))
	assert.Contains(t, logs.String(), "status=200")
	assert.Contains(t, logs.String(), "request_id=id-1")
}

func TestAccessLog_StreamKeepsFlushing(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.stream = scriptedStream(func(sink dispatch.Sink) error { return sink.Start(weakTarget) })
	srv := httptest.NewServer(newServer(t, fr, httpapi.WithAccessLog(true)).API())
	t.Cleanup(srv.Close)

	resp := post(t, srv.URL+"/v1/chat/completions", streamReq(true))
	defer resp.Body.Close()

	assert.Equal(t, "data: [DONE]\n\n", readAll(t, resp.Body))
}

type fakeDecisions struct {
	items []decision.Decision
	err   error
	limit int
	curs  string
}

func (f *fakeDecisions) List(limit int, cursor string) ([]decision.Decision, string, error) {
	f.limit, f.curs = limit, cursor
	if f.err != nil {
		return nil, "", f.err
	}
	return f.items, "next-1", nil
}

func (f *fakeDecisions) Get(id string) (decision.Decision, bool) {
	for _, d := range f.items {
		if d.ID == id {
			return d, true
		}
	}
	return decision.Decision{}, false
}

func sampleDecision() decision.Decision {
	score, thr := 0.71, 0.62
	return decision.Decision{
		ID: "d1", Route: "auto", Operation: "chat", Privacy: "mask", Findings: map[string]int{"email": 2},
		Strategy: "bert", Score: &score, Threshold: &thr, Tier: model.TierStrong,
		Reasons: []string{"strategy:bert p=0.71>=0.62"},
		Attempts: []decision.Attempt{{
			Target: "cloud", Model: "m", Locality: model.LocalityCloud, Masked: true, Status: "ok", HTTPStatus: 200,
			Usage: chat.Usage{PromptTokens: 9},
		}},
		Unmask: decision.UnmaskOK, Usage: chat.Usage{PromptTokens: 9, CompletionTokens: 1}, CostUSD: 0.01,
	}
}

func TestAdmin_Decisions_ListAndGet(t *testing.T) {
	t.Parallel()
	fd := &fakeDecisions{items: []decision.Decision{sampleDecision()}}
	h := newServer(t, newFakeRouter(), httpapi.WithDecisions(fd)).Admin()

	rec := do(t, h, http.MethodGet, "/v1/routing-decisions?limit=5&cursor=c0", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 5, fd.limit)
	assert.Equal(t, "c0", fd.curs)
	body := decodeBody(t, rec)
	assert.Equal(t, "next-1", body["next_cursor"])
	d := body["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "d1", d["id"])
	assert.Equal(t, "mask", d["privacy"])
	assert.Equal(t, map[string]any{"email": 2.0}, d["findings"])
	assert.InDelta(t, 0.71, d["score"], 1e-9)
	assert.Equal(t, "strong", d["tier"])
	att := d["attempts"].([]any)[0].(map[string]any)
	assert.Equal(t, "cloud", att["locality"])
	assert.Equal(t, true, att["masked"])

	rec = do(t, h, http.MethodGet, "/v1/routing-decisions/d1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "d1", decodeBody(t, rec)["id"])
}

func TestAdmin_Decisions_DefaultLimitAndErrors(t *testing.T) {
	t.Parallel()
	fd := &fakeDecisions{}
	h := newServer(t, newFakeRouter(), httpapi.WithDecisions(fd)).Admin()

	rec := do(t, h, http.MethodGet, "/v1/routing-decisions", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 50, fd.limit)
	assert.JSONEq(t, `{"data":[],"next_cursor":"next-1"}`, rec.Body.String())

	for _, bad := range []string{"0", "501", "x", "-1"} {
		rec = do(t, h, http.MethodGet, "/v1/routing-decisions?limit="+bad, "")
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
		assert.Equal(t, "limit", errField(t, rec, "param"))
	}

	fd.err = errors.New("bad cursor")
	rec = do(t, h, http.MethodGet, "/v1/routing-decisions?cursor=zz", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_cursor", errField(t, rec, "code"))

	rec = do(t, h, http.MethodGet, "/v1/routing-decisions/missing", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "decision_not_found", errField(t, rec, "code"))
}

func TestAdmin_OptionalEndpointsAbsentWhenNotConfigured(t *testing.T) {
	t.Parallel()
	h := newServer(t, newFakeRouter()).Admin()

	for _, p := range []string{"/v1/routing-decisions", "/v1/admin/config", "/metrics"} {
		assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, p, "").Code, p)
	}
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodPost, "/v1/admin/reload", "").Code)
}

type fakeConfig struct{}

func (fakeConfig) Redacted() any { return map[string]any{"server": map[string]any{"listen": ":8080"}} }

type fakeReloader struct{ err error }

func (f fakeReloader) Reload(context.Context) error { return f.err }

func TestAdmin_ConfigAndReload(t *testing.T) {
	t.Parallel()
	h := newServer(t, newFakeRouter(), httpapi.WithConfigView(fakeConfig{}), httpapi.WithReloader(fakeReloader{})).Admin()

	rec := do(t, h, http.MethodGet, "/v1/admin/config", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"server":{"listen":":8080"}}`, rec.Body.String())

	rec = do(t, h, http.MethodPost, "/v1/admin/reload", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"reloaded"}`, rec.Body.String())

	failing := fakeReloader{err: errors.New("missing artifact bert")}
	bad := newServer(t, newFakeRouter(), httpapi.WithReloader(failing)).Admin()
	rec = do(t, bad, http.MethodPost, "/v1/admin/reload", "")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "config_invalid", errField(t, rec, "code"))
	assert.Contains(t, errField(t, rec, "message"), "missing artifact bert")

	assert.Equal(t, http.StatusMethodNotAllowed, do(t, h, http.MethodGet, "/v1/admin/reload", "").Code)
}

type fakeHealth map[string]bool

func (f fakeHealth) IsHealthy(n string) bool { return f[n] }

func TestAdmin_ModelsHealth(t *testing.T) {
	t.Parallel()
	h := newServer(t, newFakeRouter(), httpapi.WithHealth(fakeHealth{"local-weak": false})).Admin()

	rec := do(t, h, http.MethodGet, "/v1/admin/models", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"routes":["strong","auto"],"targets":[{"name":"local-weak","provider":"lmstudio","model":"qwen",
	  "locality":"local","tier":"weak","context_window":8192,"capabilities":["tools"],
	  "healthy":false}]}`, rec.Body.String())

	plain := do(t, newServer(t, newFakeRouter()).Admin(), http.MethodGet, "/v1/admin/models", "")
	assert.NotContains(t, plain.Body.String(), "healthy")
}

func TestStandardEndpoints(t *testing.T) {
	t.Parallel()
	ready := errors.New("providers unreachable")
	var readyErr error
	s := newServer(t, newFakeRouter(), httpapi.WithReady(func(context.Context) error { return readyErr }),
		httpapi.WithMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("triage_up 1\n"))
		})))
	for name, h := range map[string]http.Handler{"api": s.API(), "admin": s.Admin()} {
		rec := do(t, h, http.MethodGet, "/healthz", "")
		assert.Equal(t, http.StatusOK, rec.Code, name)
		assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
		assert.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/readyz", "").Code, name)

		rec = do(t, h, http.MethodGet, "/v1/version", "")
		require.Equal(t, http.StatusOK, rec.Code)
		v := decodeBody(t, rec)
		assert.Contains(t, v, "version")
		assert.Contains(t, v, "commit")
		assert.Contains(t, v, "build_date")
		assert.Contains(t, v, "go_version")
	}
	assert.Equal(t, "triage_up 1\n", do(t, s.Admin(), http.MethodGet, "/metrics", "").Body.String())

	readyErr = ready
	rec := do(t, s.API(), http.MethodGet, "/readyz", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"status":"unavailable","reason":"providers unreachable"}`, rec.Body.String())
	assert.Equal(t, http.StatusOK, do(t, s.API(), http.MethodGet, "/healthz", "").Code)
}

func TestStandardEndpoints_ShuttingDown(t *testing.T) {
	t.Parallel()
	s := newServer(t, newFakeRouter())
	s.SetShuttingDown()

	rec := do(t, s.API(), http.MethodGet, "/healthz", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"status":"shutting_down"}`, rec.Body.String())
	assert.Equal(t, http.StatusServiceUnavailable, do(t, s.Admin(), http.MethodGet, "/readyz", "").Code)
}

func TestStreamWriteTimeout_AndClockOptions_Ignored_WhenInvalid(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.stream = scriptedStream(func(sink dispatch.Sink) error { return sink.Start(weakTarget) })
	s := newServer(t, fr, httpapi.WithStreamWriteTimeout(-1), httpapi.WithClock(nil), httpapi.WithStreamWriteTimeout(5e9))

	rec := do(t, s.API(), http.MethodPost, "/v1/chat/completions", streamReq(true))

	assert.Equal(t, http.StatusOK, rec.Code)
}
