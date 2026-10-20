// SPDX-License-Identifier: Apache-2.0

package httpapi_test

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

var weakTarget = model.Target{Name: "local-weak", Model: "qwen"}

const streamBody = `{"model":"auto","stream":true,"stream_options":{"include_usage":%t},` +
	`"messages":[{"role":"user","content":"hi"}]}`

func streamReq(include bool) string {
	if include {
		return strings.Replace(streamBody, "%t", "true", 1)
	}
	return strings.Replace(streamBody, "%t", "false", 1)
}

func content(s string) chat.Chunk {
	return chat.Chunk{Model: "qwen", Choices: []chat.ChunkChoice{{Delta: chat.Delta{Content: s}}}}
}

type streamFunc = func(chat.Request, dispatch.Sink) (route.Outcome, error)

func scriptedStream(steps func(sink dispatch.Sink) error) streamFunc {
	return func(_ chat.Request, sink dispatch.Sink) (route.Outcome, error) {
		if e := steps(sink); e != nil {
			return route.Outcome{}, e
		}
		return route.Outcome{DecisionID: "dec-1", Route: "auto", Target: "local-weak"}, nil
	}
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestStream_ChunksArriveInOrder_ThenDone(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	fr := newFakeRouter()
	fr.stream = scriptedStream(func(sink dispatch.Sink) error {
		if err := sink.Start(weakTarget); err != nil {
			return err
		}
		role := chat.Chunk{Model: "qwen", Choices: []chat.ChunkChoice{{Delta: chat.Delta{Role: chat.RoleAssistant}}}}
		if err := sink.Chunk(role); err != nil {
			return err
		}
		if err := sink.Chunk(content("Hel")); err != nil {
			return err
		}
		<-release // the client must already have the first events: each chunk is flushed
		if err := sink.Chunk(content("lo")); err != nil {
			return err
		}
		finish := chat.Chunk{Model: "qwen", Choices: []chat.ChunkChoice{{FinishReason: chat.FinishStop}}}
		if err := sink.Chunk(finish); err != nil {
			return err
		}
		usage := &chat.Usage{PromptTokens: 3, CompletionTokens: 2}
		return sink.Chunk(chat.Chunk{Model: "qwen", Choices: []chat.ChunkChoice{}, Usage: usage})
	})
	srv := httptest.NewServer(newServer(t, fr).API())
	t.Cleanup(srv.Close)

	resp := post(t, srv.URL+"/v1/chat/completions", streamReq(true))
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "local-weak", resp.Header.Get("X-Triage-Target"))
	assert.Equal(t, "auto", resp.Header.Get("X-Triage-Route"))
	assert.Equal(t, "id-2", resp.Header.Get("X-Triage-Decision"))
	sc := bufio.NewScanner(resp.Body)
	var events []string
	next := func() string {
		for sc.Scan() {
			if l := sc.Text(); l != "" {
				return l
			}
		}
		return ""
	}
	events = append(events, next(), next())
	assert.Contains(t, events[0], `"role":"assistant"`)
	assert.Contains(t, events[1], `"content":"Hel"`)
	close(release)
	for l := next(); l != ""; l = next() {
		events = append(events, l)
	}
	require.NoError(t, sc.Err())
	require.Len(t, events, 6)
	assert.Contains(t, events[2], `"content":"lo"`)
	assert.Contains(t, events[3], `"finish_reason":"stop"`)
	assert.Contains(t, events[4], `"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`)
	assert.Contains(t, events[4], `"choices":[]`)
	assert.Equal(t, "data: [DONE]", events[5])
	assert.Contains(t, events[1], `"object":"chat.completion.chunk"`)
	assert.Contains(t, events[1], `"id":"chatcmpl-id-2"`)
}

func TestStream_UsageHiddenWithoutIncludeUsage(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	u := &chat.Usage{PromptTokens: 3}
	fr.stream = scriptedStream(func(sink dispatch.Sink) error {
		if err := sink.Start(weakTarget); err != nil {
			return err
		}
		c := content("x")
		c.Usage = u
		if err := sink.Chunk(c); err != nil {
			return err
		}
		return sink.Chunk(chat.Chunk{Model: "qwen", Choices: []chat.ChunkChoice{}, Usage: u})
	})
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", streamReq(false))

	body := rec.Body.String()
	assert.NotContains(t, body, "usage")
	assert.Equal(t, 1+1, strings.Count(body, "data: "), "one content chunk and [DONE]")
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"))
}

func TestStream_ToolCallDeltas_Comment_AndFlush(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.stream = scriptedStream(func(sink dispatch.Sink) error {
		if err := sink.Start(weakTarget); err != nil {
			return err
		}
		if err := sink.Start(weakTarget); err != nil { // idempotent
			return err
		}
		if err := sink.Comment("note\nsecond"); err != nil {
			return err
		}
		return sink.Chunk(chat.Chunk{ID: "up", Created: 9, Model: "qwen", Choices: []chat.ChunkChoice{{
			Delta: chat.Delta{ToolCalls: []chat.ToolCallDelta{
				{Index: 0, ID: "c1", Type: "function", Name: "f", Arguments: `{"a`},
			}},
		}}})
	})
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", streamReq(true))

	body := rec.Body.String()
	assert.Contains(t, body, ": note second\n\n")
	assert.Contains(t, body,
		`"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a"}}]`)
	assert.Contains(t, body, `"id":"up","object":"chat.completion.chunk","created":9`)
	assert.True(t, rec.Flushed)
}

func TestStream_ErrorBeforeStart_RegularError(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.stream = func(chat.Request, dispatch.Sink) (route.Outcome, error) {
		return route.Outcome{DecisionID: "dec-5", Route: "auto"}, provider.ErrRateLimited
	}
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", streamReq(true))

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "rate_limit_exceeded", errField(t, rec, "code"))
	assert.Equal(t, "dec-5", rec.Header().Get("X-Triage-Decision"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestStream_ErrorAfterStart_ErrorEventWithoutDone(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.stream = func(_ chat.Request, sink dispatch.Sink) (route.Outcome, error) {
		_ = sink.Start(weakTarget)
		_ = sink.Chunk(content("part"))
		err := provider.ErrUpstreamUnavailable
		_ = sink.Error(err)
		return route.Outcome{}, err
	}
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", streamReq(true))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"content":"part"`)
	assert.Contains(t, body, `data: {"error":{"message":`)
	assert.Contains(t, body, `"code":"upstream_unavailable"`)
	assert.Equal(t, 1, strings.Count(body, `"error"`))
	assert.NotContains(t, body, "[DONE]")
}

func TestStream_CanceledAfterStart_NothingMoreWritten(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.stream = func(_ chat.Request, sink dispatch.Sink) (route.Outcome, error) {
		_ = sink.Start(weakTarget)
		return route.Outcome{}, context.Canceled
	}
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", streamReq(true))

	assert.Empty(t, rec.Body.String())
}

func TestStream_ErrorEventBeforeStart_IsNoop(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	var sinkErr error
	fr.stream = func(_ chat.Request, sink dispatch.Sink) (route.Outcome, error) {
		sinkErr = sink.Error(errors.New("x"))
		return route.Outcome{}, provider.ErrTimeout
	}
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", streamReq(true))

	require.NoError(t, sinkErr)
	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
}

func TestStream_NoPayloadSuccess_StillStartsAndTerminates(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.stream = scriptedStream(func(dispatch.Sink) error { return nil })
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", streamReq(true))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "local-weak", rec.Header().Get("X-Triage-Target"))
	assert.Equal(t, "data: [DONE]\n\n", rec.Body.String())
}

// failingWriter fails every write after the headers.
type failingWriter struct {
	*httptest.ResponseRecorder
	fail bool
}

func (w *failingWriter) Write(b []byte) (int, error) {
	if w.fail {
		return 0, errors.New("broken pipe")
	}
	return w.ResponseRecorder.Write(b)
}

func TestStream_WriteFailure_ReturnedToDispatch(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	var chunkErr, commentErr error
	fr.stream = func(_ chat.Request, sink dispatch.Sink) (route.Outcome, error) {
		_ = sink.Start(weakTarget)
		chunkErr = sink.Chunk(content("x"))
		commentErr = sink.Comment("c")
		return route.Outcome{}, chunkErr
	}
	h := newServer(t, fr).API()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions",
		strings.NewReader(streamReq(true)))
	h.ServeHTTP(&failingWriter{ResponseRecorder: httptest.NewRecorder(), fail: true}, req)

	require.Error(t, chunkErr)
	require.Error(t, commentErr)
}
