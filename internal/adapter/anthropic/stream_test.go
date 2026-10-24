// SPDX-License-Identifier: Apache-2.0

package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/anthropic"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

func TestClient_Stream_Golden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, fixture string
		includeUsage  bool
	}{
		{name: "text_usage", fixture: "stream_text", includeUsage: true},
		{name: "text_no_usage", fixture: "stream_text"},
		{name: "tool_use_usage", fixture: "stream_tool_use", includeUsage: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := userReq("x")
			req.IncludeUsage = tc.includeUsage
			s, err := newFixtureServer(t).client(t, tc.fixture).Stream(ctxT(t), req)
			require.NoError(t, err)
			defer func() { require.NoError(t, s.Close()) }()
			chunks, err := collect(t, s)
			require.ErrorIs(t, err, io.EOF)
			_, err = s.Recv()
			require.ErrorIs(t, err, io.EOF, "io.EOF is sticky")
			goldenValue(t, "stream_"+tc.name, chunks)
		})
	}
}

func TestClient_Stream_ToolUseReassembles(t *testing.T) {
	t.Parallel()
	req := userReq("x")
	req.IncludeUsage = true
	s, err := newFixtureServer(t).client(t, "stream_tool_use").Stream(ctxT(t), req)
	require.NoError(t, err)
	defer s.Close()
	chunks, err := collect(t, s)
	require.ErrorIs(t, err, io.EOF)
	args := map[int]*strings.Builder{}
	names := map[int]string{}
	var text strings.Builder
	var finish chat.FinishReason
	for _, c := range chunks {
		assert.Equal(t, "msg_01StreamTool", c.ID)
		for _, cc := range c.Choices {
			text.WriteString(cc.Delta.Content)
			if cc.FinishReason != "" {
				finish = cc.FinishReason
			}
			for _, d := range cc.Delta.ToolCalls {
				if args[d.Index] == nil {
					args[d.Index] = &strings.Builder{}
				}
				args[d.Index].WriteString(d.Arguments)
				if d.Name != "" {
					names[d.Index] = d.Name
				}
			}
		}
	}
	assert.Equal(t, "Checking.", text.String())
	assert.Equal(t, map[int]string{0: "get_weather", 1: "get_time"}, names)
	assert.JSONEq(t, `{"city":"Hanoi"}`, args[0].String())
	assert.JSONEq(t, `{}`, args[1].String())
	assert.Equal(t, chat.FinishToolCalls, finish)
	last := chunks[len(chunks)-1]
	require.NotNil(t, last.Usage)
	assert.Equal(t, chat.Usage{PromptTokens: 450, CompletionTokens: 58, CachedPromptTokens: 50}, *last.Usage)
	assert.Empty(t, last.Choices)
}

func TestClient_Stream_Failures_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fixture string
		chunks  int
		kind    error
	}{
		{fixture: "stream_overloaded", chunks: 2, kind: provider.ErrUpstreamUnavailable},
		{fixture: "stream_cut", chunks: 2, kind: provider.ErrUpstreamUnavailable},
		{fixture: "stream_malformed", kind: provider.ErrUpstreamUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			s, err := newFixtureServer(t).client(t, tc.fixture).Stream(ctxT(t), userReq("x"))
			require.NoError(t, err)
			defer s.Close()
			chunks, err := collect(t, s)
			require.ErrorIs(t, err, tc.kind)
			assert.Equal(t, 200, provider.StatusCode(err))
			assert.Len(t, chunks, tc.chunks)
		})
	}
}

func sseServer(t *testing.T, body string) string {
	t.Helper()
	return newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	})
}

const startEvent = `data: {"type":"message_start","message":{"id":"m","model":"c","usage":{"input_tokens":1}}}` + "\n\n"

func TestClient_Stream_Events_Table(t *testing.T) {
	t.Parallel()
	stop := `data: {"type":"message_stop"}` + "\n\n"
	tests := []struct {
		name   string
		body   string
		max    int
		chunks int
		kind   error
	}{
		{name: "unknown events skipped", body: startEvent + `data: {"type":"future_event"}` + "\n\n" +
			`data: {"type":"message_delta","usage":{"output_tokens":3}}` + "\n\n" + stop, chunks: 1, kind: io.EOF},
		{
			name: "start without message", body: `data: {"type":"message_start"}` + "\n\n",
			kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "block start without block", body: startEvent + `data: {"type":"content_block_start","index":0}` + "\n\n",
			chunks: 1, kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "unknown block type skipped", body: startEvent +
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use"}}` + "\n\n" + stop,
			chunks: 1, kind: io.EOF,
		},
		{
			name: "delta without delta", body: startEvent + `data: {"type":"content_block_delta","index":0}` + "\n\n",
			chunks: 1, kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "json delta for unknown block", body: startEvent +
				`data: {"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{"}}` + "\n\n",
			chunks: 1, kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "empty text delta skipped", body: startEvent +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}` + "\n\n" + stop,
			chunks: 1, kind: io.EOF,
		},
		{
			name: "rate limit error event", body: `data: {"type":"error","error":{"type":"rate_limit_error"}}` + "\n\n",
			kind: provider.ErrRateLimited,
		},
		{
			name: "timeout error event", body: `data: {"type":"error","error":{"type":"timeout_error"}}` + "\n\n",
			kind: provider.ErrTimeout,
		},
		{name: "prompt too long error event", body: `data: {"type":"error","error":{"type":"invalid_request_error",` +
			`"message":"prompt is too long"}}` + "\n\n", kind: provider.ErrContextLength},
		{name: "invalid request error event", body: `data: {"type":"error","error":{"type":"invalid_request_error"}}` +
			"\n\n", kind: provider.ErrBadRequest},
		{
			name: "error event without object", body: `data: {"type":"error"}` + "\n\n",
			kind: provider.ErrUpstreamUnavailable,
		},
		{
			name: "line too large", body: startEvent + "data: " + strings.Repeat("x", 300) + "\n\n", max: 200,
			chunks: 1, kind: provider.ErrUpstreamUnavailable,
		},
		{name: "no data at all", body: ": comment\n\n", kind: provider.ErrUpstreamUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := []anthropic.Option{anthropic.WithBaseURL(sseServer(t, tc.body))}
			if tc.max > 0 {
				opts = append(opts, anthropic.WithMaxResponseBytes(tc.max))
			}
			s, err := newClient(t, opts...).Stream(ctxT(t), userReq("x"))
			require.NoError(t, err)
			defer s.Close()
			chunks, err := collect(t, s)
			require.ErrorIs(t, err, tc.kind)
			assert.Len(t, chunks, tc.chunks)
		})
	}
}

func TestClient_Stream_CancelMidStream(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	base := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, startEvent)
		http.NewResponseController(w).Flush() //nolint:errcheck // test server
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(ctxT(t))
	s, err := newClient(t, anthropic.WithBaseURL(base)).Stream(ctx, userReq("x"))
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Recv()
	require.NoError(t, err)
	cancel()
	_, err = s.Recv()
	require.ErrorIs(t, err, context.Canceled)
}

func TestClient_Stream_CloseIdempotent(t *testing.T) {
	t.Parallel()
	s, err := newFixtureServer(t).client(t, "stream_text").Stream(ctxT(t), userReq("x"))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	_, err = s.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}
