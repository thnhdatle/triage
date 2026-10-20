// SPDX-License-Identifier: Apache-2.0

package openaicompat_test

import (
	"encoding/base64"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/openaicompat"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

// fixed returns a handler answering with status, headers and body.
func fixed(status int, headers map[string]string, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

var epoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func TestClient_Complete_StatusMapping_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		headers    map[string]string
		body       string
		kind       error
		retryAfter time.Duration
	}{
		{
			name: "context length code", status: 400, kind: provider.ErrContextLength,
			body: `{"error":{"message":"too long","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		},
		{
			name: "context length message (lm studio)", status: 400, kind: provider.ErrContextLength,
			body: `{"error":{"message":"Trying to keep the first 9000 tokens when context window overflows"}}`,
		},
		{name: "error not an object", status: 400, kind: provider.ErrBadRequest, body: `{"error":"x"}`},
		{
			name: "context length message object", status: 400, kind: provider.ErrContextLength,
			body: `{"error":{"message":"The number of tokens to keep from the initial prompt is greater than the ` +
				`context length","code":null}}`,
		},
		{
			name: "numeric code", status: 422, kind: provider.ErrBadRequest,
			body: `{"error":{"message":"bad","type":"invalid_request_error","code":42}}`,
		},
		{name: "unauthorized", status: 401, kind: provider.ErrBadRequest, body: `{"error":{"type":"auth"}}`},
		{name: "not found plain text", status: 404, kind: provider.ErrBadRequest, body: `not found`},
		{name: "request timeout", status: 408, kind: provider.ErrTimeout},
		{name: "gateway timeout", status: 504, kind: provider.ErrTimeout},
		{name: "bad gateway", status: 502, kind: provider.ErrUpstreamUnavailable},
		{name: "overloaded", status: 529, kind: provider.ErrUpstreamUnavailable},
		{
			name: "429 seconds", status: 429, headers: map[string]string{"Retry-After": "7"},
			kind: provider.ErrRateLimited, retryAfter: 7 * time.Second,
		},
		{
			name: "429 fractional", status: 429, headers: map[string]string{"Retry-After": "0.5"},
			kind: provider.ErrRateLimited, retryAfter: 500 * time.Millisecond,
		},
		{
			name: "429 ms header wins", status: 429, headers: map[string]string{"Retry-After": "7", "Retry-After-Ms": "250"},
			kind: provider.ErrRateLimited, retryAfter: 250 * time.Millisecond,
		},
		{
			name: "429 http date", status: 429,
			headers: map[string]string{"Retry-After": epoch.Add(30 * time.Second).Format(http.TimeFormat)},
			kind:    provider.ErrRateLimited, retryAfter: 30 * time.Second,
		},
		{
			name: "429 past date", status: 429,
			headers: map[string]string{"Retry-After": epoch.Add(-time.Minute).Format(http.TimeFormat)},
			kind:    provider.ErrRateLimited,
		},
		{
			name: "429 negative", status: 429, headers: map[string]string{"Retry-After": "-3"},
			kind: provider.ErrRateLimited,
		},
		{
			name: "429 garbage", status: 429, headers: map[string]string{"Retry-After": "soon", "Retry-After-Ms": "x"},
			kind: provider.ErrRateLimited,
		},
		{name: "429 none", status: 429, kind: provider.ErrRateLimited},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, newServer(t, fixed(tc.status, tc.headers, tc.body)),
				openaicompat.WithClock(clock.NewFake(epoch)))
			_, err := c.Complete(ctxT(t), userReq("m", "x"))
			require.ErrorIs(t, err, tc.kind)
			assert.Equal(t, tc.status, provider.StatusCode(err))
			assert.Equal(t, tc.retryAfter, provider.RetryAfter(err))
		})
	}
}

func TestClient_Complete_MalformedBodies_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "not json", body: `<html>`},
		{name: "no choices", body: `{"id":"x","choices":[]}`},
		{name: "bad content", body: `{"choices":[{"message":{"content":42}}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, newServer(t, fixed(200, nil, tc.body)))
			_, err := c.Complete(ctxT(t), userReq("m", "x"))
			require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
			assert.Equal(t, 200, provider.StatusCode(err))
		})
	}
}

func TestClient_Complete_ResponseShapes(t *testing.T) {
	t.Parallel()
	body := `{"id":"r","model":"up","created":5,"system_fingerprint":"fp",` +
		`"choices":[{"index":0,"message":{"content":[{"type":"text","text":"a"},{"type":"x"},` +
		`{"type":"text","text":"b"}]},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`
	c := newClient(t, newServer(t, fixed(200, nil, body)))
	resp, err := c.Complete(ctxT(t), userReq("m", "x"))
	require.NoError(t, err)
	assert.Equal(t, chat.Response{
		ID: "r", Model: "up", Created: 5, SystemFingerprint: "fp",
		Choices: []chat.Choice{{
			Message: chat.Message{
				Role: chat.RoleAssistant, ContentIsString: true, Content: []chat.Part{{Type: chat.PartText, Text: "ab"}},
			},
			FinishReason: chat.FinishLength,
		}},
		Usage: chat.Usage{PromptTokens: 10, CompletionTokens: 2, CachedPromptTokens: 4},
	}, resp)
}

func TestClient_Complete_BodyTooLarge(t *testing.T) {
	t.Parallel()
	c := newClient(t, newServer(t, fixed(200, nil, okBody)), openaicompat.WithMaxResponseBytes(10))
	_, err := c.Complete(ctxT(t), userReq("m", "x"))
	require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
}

func sse(events ...string) http.HandlerFunc {
	return fixed(200, map[string]string{"Content-Type": "text/event-stream"}, strings.Join(events, ""))
}

func TestClient_Stream_SSEParsing_Table(t *testing.T) {
	t.Parallel()
	chunk := `{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`
	tests := []struct {
		name   string
		body   string
		max    int64
		chunks int
		kind   error
	}{
		{name: "comments and fields ignored", body: ": keep-alive\n\nevent: chunk\nid: 1\ndata: " + chunk +
			"\r\n\r\ndata: [DONE]\n\n", chunks: 1, kind: io.EOF},
		{name: "done without trailing newline", body: "data: " + chunk + "\n\ndata: [DONE]", chunks: 1, kind: io.EOF},
		{
			name: "multi-line data", body: "data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n",
			chunks: 1, kind: io.EOF,
		},
		{name: "empty events skipped", body: "data: {}\n\ndata: {\"choices\":[]}\n\ndata: [DONE]\n\n", kind: io.EOF},
		{name: "eof without done", body: "data: " + chunk + "\n\n", chunks: 1, kind: provider.ErrUpstreamUnavailable},
		{name: "eof mid event", body: "data: " + chunk + "\n", kind: provider.ErrUpstreamUnavailable},
		{name: "event too large", body: "data: " + chunk + "\n\n", max: 16, kind: provider.ErrUpstreamUnavailable},
		{
			name: "data line too large", body: "data: " + strings.Repeat("x", 5000) + "\n\n", max: 4500,
			kind: provider.ErrUpstreamUnavailable,
		},
		{name: "multi-line too large", body: "data: " + strings.Repeat("x", 3000) + "\ndata: " +
			strings.Repeat("x", 3000) + "\n\n", max: 4500, kind: provider.ErrUpstreamUnavailable},
		{
			name: "rate limit event", body: `data: {"error":{"type":"rate_limit_error","message":"slow"}}` + "\n\n",
			kind: provider.ErrRateLimited,
		},
		{
			name: "context length event", body: `data: {"error":{"code":"context_length_exceeded"}}` + "\n\n",
			kind: provider.ErrContextLength,
		},
		{
			name: "invalid request event", body: `data: {"error":{"type":"invalid_request_error"}}` + "\n\n",
			kind: provider.ErrBadRequest,
		},
		{
			name: "server error event", body: `data: {"error":{"message":"boom"}}` + "\n\n",
			kind: provider.ErrUpstreamUnavailable,
		},
		{name: "null error is not an error", body: `data: {"error":null,"choices":[{"delta":{"content":"a"}}]}` +
			"\n\ndata: [DONE]\n\n", chunks: 1, kind: io.EOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := []openaicompat.Option{}
			if tc.max > 0 {
				opts = append(opts, openaicompat.WithMaxResponseBytes(tc.max))
			}
			c := newClient(t, newServer(t, sse(tc.body)), opts...)
			s, err := c.Stream(ctxT(t), userReq("m", "x"))
			require.NoError(t, err)
			defer s.Close()
			chunks, err := collect(t, s)
			require.ErrorIs(t, err, tc.kind)
			assert.Len(t, chunks, tc.chunks)
		})
	}
}

func TestClient_Stream_ChunkTranslation(t *testing.T) {
	t.Parallel()
	body := `data: {"id":"c1","model":"up","created":9,"choices":[{"index":1,"delta":{"role":"assistant",` +
		`"tool_calls":[{"index":2,"id":"t","type":"function","function":{"name":"f","arguments":"{"}}]},` +
		`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}` + "\n\ndata: [DONE]\n\n"
	c := newClient(t, newServer(t, sse(body)))
	s, err := c.Stream(ctxT(t), userReq("m", "x"))
	require.NoError(t, err)
	defer s.Close()
	got, err := s.Recv()
	require.NoError(t, err)
	assert.Equal(t, chat.Chunk{
		ID: "c1", Model: "up", Created: 9, Usage: &chat.Usage{PromptTokens: 3, CompletionTokens: 1},
		Choices: []chat.ChunkChoice{{Index: 1, FinishReason: chat.FinishToolCalls, Delta: chat.Delta{
			Role:      chat.RoleAssistant,
			ToolCalls: []chat.ToolCallDelta{{Index: 2, ID: "t", Type: "function", Name: "f", Arguments: "{"}},
		}}},
	}, got)
}

func float32Base64(v ...float32) string {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestClient_Embed_Shapes_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		body  string
		input []string
		want  [][]float32
		kind  error
	}{
		{
			name: "out of order", input: []string{"a", "b"},
			body: `{"model":"e","data":[{"index":1,"embedding":[2]},{"index":0,"embedding":[1]}]}`,
			want: [][]float32{{1}, {2}},
		},
		{
			name: "base64", input: []string{"a"},
			body: `{"model":"e","data":[{"index":0,"embedding":"` + float32Base64(0.5, -1) + `"}]}`,
			want: [][]float32{{0.5, -1}},
		},
		{
			name: "count mismatch", input: []string{"a", "b"}, body: `{"data":[{"index":0,"embedding":[1]}]}`,
			kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "duplicate index", input: []string{"a", "b"},
			body: `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[1]}]}`, kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "index out of range", input: []string{"a"}, body: `{"data":[{"index":3,"embedding":[1]}]}`,
			kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "bad base64", input: []string{"a"}, body: `{"data":[{"index":0,"embedding":"!!"}]}`,
			kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "base64 wrong length", input: []string{"a"}, body: `{"data":[{"index":0,"embedding":"AAA="}]}`,
			kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "null embedding", input: []string{"a"}, body: `{"data":[{"index":0,"embedding":null}]}`,
			kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "number embedding", input: []string{"a"}, body: `{"data":[{"index":0,"embedding":5}]}`,
			kind: provider.ErrUpstreamUnavailable,
		},
		{name: "not json", input: []string{"a"}, body: `nope`, kind: provider.ErrUpstreamUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, newServer(t, fixed(200, nil, tc.body)))
			resp, err := c.Embed(ctxT(t), provider.EmbedRequest{Model: "e", Input: tc.input, EncodingFormat: "base64"})
			if tc.kind != nil {
				require.ErrorIs(t, err, tc.kind)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.Vectors)
		})
	}
}

func TestClient_Embed_Unavailable(t *testing.T) {
	t.Parallel()
	c := newClient(t, newServer(t, fixed(503, nil, "")))
	_, err := c.Embedder().Embed(ctxT(t), "e", 0, []string{"a"})
	require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
}

func TestClient_Models_Errors_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		kind   error
	}{
		{name: "malformed", status: 200, body: `{}`, kind: provider.ErrUpstreamUnavailable},
		{name: "not json", status: 200, body: `x`, kind: provider.ErrUpstreamUnavailable},
		{name: "unauthorized", status: 401, kind: provider.ErrBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, newServer(t, fixed(tc.status, nil, tc.body)))
			_, err := c.Models(ctxT(t))
			require.ErrorIs(t, err, tc.kind)
		})
	}
}

func TestNew_Config_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		base string
		opts []openaicompat.Option
	}{
		{name: "relative url", base: "/v1"},
		{name: "bad scheme", base: "ftp://x/v1"},
		{name: "unparsable", base: "http://[::1"},
		{name: "zero timeout", base: "http://x", opts: []openaicompat.Option{openaicompat.WithTimeout(0)}},
		{name: "negative idle", base: "http://x", opts: []openaicompat.Option{openaicompat.WithMaxIdleConnsPerHost(-1)}},
		{name: "zero max bytes", base: "http://x", opts: []openaicompat.Option{openaicompat.WithMaxResponseBytes(0)}},
		{name: "nil clock", base: "http://x", opts: []openaicompat.Option{openaicompat.WithClock(nil)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := openaicompat.New(tc.base, tc.opts...)
			require.ErrorIs(t, err, openaicompat.ErrConfig)
		})
	}
}

func TestNew_HTTPClientAndTrailingSlash(t *testing.T) {
	t.Parallel()
	cs := &captureServer{}
	hc := &http.Client{}
	t.Cleanup(hc.CloseIdleConnections)
	c := newClient(t, newServer(t, cs.handle)+"/v1/", openaicompat.WithHTTPClient(hc),
		openaicompat.WithMaxIdleConnsPerHost(2))
	_, err := c.Complete(ctxT(t), userReq("m", "x"))
	require.NoError(t, err)
}
