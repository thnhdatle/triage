// SPDX-License-Identifier: Apache-2.0

package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/openaicompat"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/fakellm"
)

func TestClient_Complete_Fakellm(t *testing.T) {
	t.Parallel()
	base := newFake(t)
	c := newClient(t, base, scenario(fakellm.ScenarioEcho, nil), openaicompat.WithAPIKey("k"))
	resp, err := c.Complete(ctxT(t), userReq("weak-model", "one two three"))
	require.NoError(t, err)
	assert.Equal(t, "weak-model", resp.Model)
	assert.NotEmpty(t, resp.ID)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "one two three", resp.Choices[0].Message.Text())
	assert.Equal(t, chat.RoleAssistant, resp.Choices[0].Message.Role)
	assert.Equal(t, chat.FinishStop, resp.Choices[0].FinishReason)
	assert.Equal(t, chat.Usage{PromptTokens: 3, CompletionTokens: 3}, resp.Usage)

	recs := fakeRequests(t, base)
	require.Len(t, recs, 1)
	assert.Equal(t, "/v1/chat/completions", recs[0].Path)
	assert.Equal(t, "Bearer k", recs[0].Headers.Get("Authorization"))
}

func TestClient_Complete_ToolCall(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario(fakellm.ScenarioToolCall, nil))
	req := userReq("m", "Hanoi")
	req.Tools = []chat.Tool{{Type: "function", Function: chat.ToolFunction{Name: "lookup"}}}
	resp, err := c.Complete(ctxT(t), req)
	require.NoError(t, err)
	msg := resp.Choices[0].Message
	assert.Empty(t, msg.Content)
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, "lookup", msg.ToolCalls[0].Function.Name)
	assert.Equal(t, "function", msg.ToolCalls[0].Type)
	assert.JSONEq(t, `{"input":"Hanoi"}`, msg.ToolCalls[0].Function.Arguments)
	assert.Equal(t, chat.FinishToolCalls, resp.Choices[0].FinishReason)
}

func TestClient_Complete_ErrorScenarios(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		scenario   string
		kind       error
		status     int
		retryAfter time.Duration
	}{
		{name: "rate limited", scenario: "429", kind: provider.ErrRateLimited, status: 429, retryAfter: time.Second},
		{name: "server error", scenario: "500", kind: provider.ErrUpstreamUnavailable, status: 500},
		{name: "invalid json", scenario: fakellm.ScenarioInvalidJSON, kind: provider.ErrUpstreamUnavailable, status: 200},
		{name: "cut", scenario: fakellm.ScenarioCutStream, kind: provider.ErrUpstreamUnavailable},
		{name: "unknown scenario is bad request", scenario: "nope", kind: provider.ErrBadRequest, status: 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, newFake(t), scenario(tc.scenario, nil))
			_, err := c.Complete(ctxT(t), userReq("m", "x"))
			require.ErrorIs(t, err, tc.kind)
			assert.Equal(t, tc.status, provider.StatusCode(err))
			assert.Equal(t, tc.retryAfter, provider.RetryAfter(err))
		})
	}
}

func TestClient_Complete_ErrorMessageHasNoContent(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario("secret-scenario-text", nil))
	_, err := c.Complete(ctxT(t), userReq("m", "x"))
	require.ErrorIs(t, err, provider.ErrBadRequest)
	assert.NotContains(t, err.Error(), "secret-scenario-text")
	assert.Contains(t, err.Error(), "upstream status 400 type invalid_request_error")
}

func TestClient_Complete_Timeout(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario(fakellm.ScenarioTimeout, nil),
		openaicompat.WithTimeout(100*time.Millisecond))
	_, err := c.Complete(ctxT(t), userReq("m", "x"))
	require.ErrorIs(t, err, provider.ErrTimeout)
	_, err = c.Stream(ctxT(t), userReq("m", "x"))
	require.ErrorIs(t, err, provider.ErrTimeout)
}

func TestClient_Complete_ContextCanceled(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario(fakellm.ScenarioTimeout, nil))
	cause := errors.New("attempt timeout")
	ctx, cancel := context.WithCancelCause(ctxT(t))
	time.AfterFunc(50*time.Millisecond, func() { cancel(cause) })
	_, err := c.Complete(ctx, userReq("m", "x"))
	require.ErrorIs(t, err, cause)

	ctx, cancel2 := context.WithCancel(ctxT(t))
	cancel2()
	_, err = c.Stream(ctx, userReq("m", "x"))
	require.ErrorIs(t, err, context.Canceled)
}

func TestClient_Complete_ContextDeadline(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario(fakellm.ScenarioTimeout, nil))
	ctx, cancel := context.WithTimeout(ctxT(t), 50*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, userReq("m", "x"))
	require.ErrorIs(t, err, provider.ErrTimeout)
}

func TestClient_Complete_Unreachable(t *testing.T) {
	t.Parallel()
	c := newClient(t, "http://127.0.0.1:1/v1")
	_, err := c.Complete(ctxT(t), userReq("m", "x"))
	require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
	assert.True(t, provider.IsRetryable(err))
}

func collect(t *testing.T, s provider.Stream) ([]chat.Chunk, error) {
	t.Helper()
	var out []chat.Chunk
	for {
		c, err := s.Recv()
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
}

func TestClient_Stream_TextWithUsage(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario(fakellm.ScenarioEcho, map[string]string{"X-Fake-Chunk-Bytes": "4"}))
	req := userReq("m", "hello streaming world")
	req.IncludeUsage = true
	s, err := c.Stream(ctxT(t), req)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	chunks, err := collect(t, s)
	require.ErrorIs(t, err, io.EOF)
	var text strings.Builder
	var usage *chat.Usage
	var finish chat.FinishReason
	for _, ch := range chunks {
		assert.Equal(t, "m", ch.Model)
		for _, cc := range ch.Choices {
			text.WriteString(cc.Delta.Content)
			if cc.FinishReason != "" {
				finish = cc.FinishReason
			}
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
	}
	assert.Equal(t, "hello streaming world", text.String())
	assert.Equal(t, chat.RoleAssistant, chunks[0].Choices[0].Delta.Role)
	assert.Equal(t, chat.FinishStop, finish)
	require.NotNil(t, usage)
	assert.Equal(t, chat.Usage{PromptTokens: 3, CompletionTokens: 3}, *usage)
	_, err = s.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestClient_Stream_ToolCall(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario(fakellm.ScenarioToolCall, map[string]string{"X-Fake-Chunk-Bytes": "3"}))
	req := userReq("m", "Hanoi")
	req.Tools = []chat.Tool{{Type: "function", Function: chat.ToolFunction{Name: "lookup"}}}
	s, err := c.Stream(ctxT(t), req)
	require.NoError(t, err)
	defer s.Close()
	chunks, err := collect(t, s)
	require.ErrorIs(t, err, io.EOF)
	var args strings.Builder
	var name, id string
	for _, ch := range chunks {
		for _, cc := range ch.Choices {
			for _, d := range cc.Delta.ToolCalls {
				assert.Equal(t, 0, d.Index)
				args.WriteString(d.Arguments)
				if d.Name != "" {
					name, id = d.Name, d.ID
				}
			}
		}
	}
	assert.Equal(t, "lookup", name)
	assert.NotEmpty(t, id)
	assert.JSONEq(t, `{"input":"Hanoi"}`, args.String())
	assert.Nil(t, chunks[len(chunks)-1].Usage, "usage not requested")
}

func TestClient_Stream_Failures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		scenario string
		headers  map[string]string
		chunks   int
		kind     error
		before   bool
	}{
		{name: "cut after 2", scenario: fakellm.ScenarioCutStream, headers: map[string]string{
			"X-Fake-Cut-After": "2", "X-Fake-Chunk-Bytes": "2",
		}, chunks: 2, kind: provider.ErrUpstreamUnavailable},
		{name: "cut before headers", scenario: fakellm.ScenarioCutStream, headers: map[string]string{
			"X-Fake-Cut-After": "0",
		}, kind: provider.ErrUpstreamUnavailable, before: true},
		{name: "malformed event", scenario: fakellm.ScenarioInvalidJSON, kind: provider.ErrUpstreamUnavailable},
		{name: "rate limited", scenario: "429", kind: provider.ErrRateLimited, before: true},
		{name: "server error", scenario: "500", kind: provider.ErrUpstreamUnavailable, before: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, newFake(t), scenario(tc.scenario, tc.headers))
			s, err := c.Stream(ctxT(t), userReq("m", "a b c d e f g h"))
			if tc.before {
				require.ErrorIs(t, err, tc.kind)
				return
			}
			require.NoError(t, err)
			defer s.Close()
			chunks, err := collect(t, s)
			require.ErrorIs(t, err, tc.kind)
			assert.Len(t, chunks, tc.chunks)
			_, again := s.Recv()
			assert.Equal(t, err, again, "error is sticky")
		})
	}
}

func TestClient_Stream_CancelMidStream(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t), scenario(fakellm.ScenarioEcho, map[string]string{
		"X-Fake-Chunk-Bytes": "1", "X-Fake-Chunk-Delay-Ms": "200",
	}))
	ctx, cancel := context.WithCancel(ctxT(t))
	s, err := c.Stream(ctx, userReq("m", "abcdef"))
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Recv()
	require.NoError(t, err)
	cancel()
	_, err = collect(t, s)
	require.ErrorIs(t, err, context.Canceled)
}

func TestClient_Stream_CloseIdempotent(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t))
	s, err := c.Stream(ctxT(t), userReq("m", "abc"))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	_, err = s.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}

func TestClient_Embed_Fakellm(t *testing.T) {
	t.Parallel()
	base := newFake(t)
	c := newClient(t, base)
	resp, err := c.Embed(ctxT(t), provider.EmbedRequest{
		Model: "text-embedding-3-small", Input: []string{"a b", "c"}, Dimensions: 8, User: "u",
	})
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-small", resp.Model)
	require.Len(t, resp.Vectors, 2)
	assert.Len(t, resp.Vectors[0], 8)
	assert.NotEqual(t, resp.Vectors[0], resp.Vectors[1])
	assert.Equal(t, 3, resp.Usage.PromptTokens)

	vecs, err := c.Embedder().Embed(ctxT(t), "text-embedding-3-small", 8, []string{"a b"})
	require.NoError(t, err)
	assert.Equal(t, resp.Vectors[:1], vecs)

	recs := fakeRequests(t, base)
	require.Len(t, recs, 2)
	assert.Equal(t, fakellm.KindEmbeddings, recs[0].Kind)
	assert.Equal(t, 2, recs[0].InputCount)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(recs[0].Body), &body))
	assert.InDelta(t, 8, body["dimensions"], 0)
	assert.Equal(t, "u", body["user"])
}

func TestClient_Embed_Errors(t *testing.T) {
	t.Parallel()
	c := newClient(t, newFake(t))
	_, err := c.Embedder().Embed(ctxT(t), "m", 99999, []string{"x"})
	require.ErrorIs(t, err, provider.ErrBadRequest)
}

func TestClient_Models_Fakellm(t *testing.T) {
	t.Parallel()
	base := newFake(t)
	c := newClient(t, base)
	_, err := c.Complete(ctxT(t), userReq("seen-model", "x"))
	require.NoError(t, err)
	names, err := c.Models(ctxT(t))
	require.NoError(t, err)
	assert.Equal(t, []string{fakellm.DefaultModel, "seen-model"}, names)
}
