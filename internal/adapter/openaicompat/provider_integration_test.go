// SPDX-License-Identifier: Apache-2.0

//go:build integration

package openaicompat_test

import (
	"context"
	"encoding/json"
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

func containerRequests(t *testing.T, model string) []fakellm.Record {
	t.Helper()
	recs, dropped, err := container.Requests(ctxT(t), model)
	require.NoError(t, err)
	require.Zero(t, dropped)
	return recs
}

func TestIntegration_Complete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		model    string
		scenario string
		want     string
	}{
		{name: "echo", model: "it-complete-echo", scenario: fakellm.ScenarioEcho, want: "one two three"},
		{name: "ok is deterministic", model: "it-complete-ok", scenario: fakellm.ScenarioOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, container.BaseURL(), scenario(tc.scenario, nil), openaicompat.WithAPIKey("k"))
			resp, err := c.Complete(ctxT(t), userReq(tc.model, "one two three"))
			require.NoError(t, err)
			assert.Equal(t, tc.model, resp.Model)
			require.Len(t, resp.Choices, 1)
			assert.Equal(t, chat.FinishStop, resp.Choices[0].FinishReason)
			assert.Equal(t, 3, resp.Usage.PromptTokens)
			text := resp.Choices[0].Message.Text()
			if tc.want != "" {
				assert.Equal(t, tc.want, text)
			} else {
				again, err := c.Complete(ctxT(t), userReq(tc.model, "one two three"))
				require.NoError(t, err)
				assert.Len(t, strings.Fields(text), 6)
				assert.Equal(t, text, again.Choices[0].Message.Text(), "same model and messages give the same text")
			}

			recs := containerRequests(t, tc.model)
			require.NotEmpty(t, recs)
			assert.Equal(t, "/v1/chat/completions", recs[0].Path)
			assert.Equal(t, fakellm.KindChat, recs[0].Kind)
			assert.Equal(t, "Bearer k", recs[0].Headers.Get("Authorization"))
		})
	}
}

func TestIntegration_Complete_ToolCall(t *testing.T) {
	t.Parallel()
	c := newClient(t, container.BaseURL(), scenario(fakellm.ScenarioToolCall, nil))
	req := userReq("it-tool", "Hanoi")
	req.Tools = []chat.Tool{{Type: "function", Function: chat.ToolFunction{Name: "lookup"}}}
	resp, err := c.Complete(ctxT(t), req)
	require.NoError(t, err)
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, "lookup", resp.Choices[0].Message.ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"input":"Hanoi"}`, resp.Choices[0].Message.ToolCalls[0].Function.Arguments)
	assert.Equal(t, chat.FinishToolCalls, resp.Choices[0].FinishReason)
}

func TestIntegration_Stream(t *testing.T) {
	t.Parallel()
	c := newClient(t, container.BaseURL(),
		scenario(fakellm.ScenarioEcho, map[string]string{"X-Fake-Chunk-Bytes": "3"}))
	req := userReq("it-stream", "hello streaming world across docker")
	req.IncludeUsage = true
	s, err := c.Stream(ctxT(t), req)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	chunks, err := collect(t, s)
	require.ErrorIs(t, err, io.EOF)
	require.Greater(t, len(chunks), 3, "chunked over the network")

	var text strings.Builder
	var usage *chat.Usage
	var finish chat.FinishReason
	for _, ch := range chunks {
		assert.Equal(t, "it-stream", ch.Model)
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
	assert.Equal(t, "hello streaming world across docker", text.String())
	assert.Equal(t, chat.FinishStop, finish)
	require.NotNil(t, usage)
	assert.Equal(t, chat.Usage{PromptTokens: 5, CompletionTokens: 5}, *usage)

	recs := containerRequests(t, "it-stream")
	require.Len(t, recs, 1)
	var body struct {
		Stream        bool `json:"stream"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	require.NoError(t, json.Unmarshal([]byte(recs[0].Body), &body))
	assert.True(t, body.Stream)
	assert.True(t, body.StreamOptions.IncludeUsage)
}

func TestIntegration_Stream_CutMidStream(t *testing.T) {
	t.Parallel()
	c := newClient(t, container.BaseURL(), scenario(fakellm.ScenarioCutStream, map[string]string{
		"X-Fake-Cut-After": "2", "X-Fake-Chunk-Bytes": "2", "X-Fake-Answer": "0123456789",
	}))
	s, err := c.Stream(ctxT(t), userReq("it-cut", "a b c d"))
	require.NoError(t, err)
	defer s.Close()
	chunks, err := collect(t, s)
	require.ErrorIs(t, err, provider.ErrUpstreamUnavailable, "a cut connection is a retryable upstream failure")
	assert.True(t, provider.IsRetryable(err))
	require.Len(t, chunks, 2)
	var text strings.Builder
	for _, ch := range chunks {
		for _, cc := range ch.Choices {
			text.WriteString(cc.Delta.Content)
		}
	}
	assert.Equal(t, "0123", text.String(), "the chunks before the cut are delivered")
	_, again := s.Recv()
	assert.Equal(t, err, again, "error is sticky")
}

func TestIntegration_Stream_CancelMidStream(t *testing.T) {
	t.Parallel()
	c := newClient(t, container.BaseURL(), scenario(fakellm.ScenarioEcho, map[string]string{
		"X-Fake-Chunk-Bytes": "1", "X-Fake-Chunk-Delay-Ms": "200",
	}))
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	s, err := c.Stream(ctx, userReq("it-cancel", "abcdef"))
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Recv()
	require.NoError(t, err)
	cancel()
	_, err = collect(t, s)
	require.ErrorIs(t, err, context.Canceled)
}

func TestIntegration_ErrorClasses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		scenario   string
		kind       error
		status     int
		retryAfter time.Duration
		retryable  bool
	}{
		{
			name: "429 is rate limited with Retry-After", scenario: fakellm.Scenario429,
			kind: provider.ErrRateLimited, status: 429, retryAfter: time.Second, retryable: true,
		},
		{
			name: "500 is upstream unavailable", scenario: fakellm.Scenario500,
			kind: provider.ErrUpstreamUnavailable, status: 500, retryable: true,
		},
		{
			name: "truncated body is upstream unavailable", scenario: fakellm.ScenarioInvalidJSON,
			kind: provider.ErrUpstreamUnavailable, status: 200, retryable: true,
		},
		{
			name: "connection reset is upstream unavailable", scenario: fakellm.ScenarioCutStream,
			kind: provider.ErrUpstreamUnavailable, retryable: true,
		},
		{
			name: "400 is bad request", scenario: "no-such-scenario",
			kind: provider.ErrBadRequest, status: 400,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClient(t, container.BaseURL(), scenario(tc.scenario, nil))
			_, err := c.Complete(ctxT(t), userReq("it-errors", "x"))
			require.ErrorIs(t, err, tc.kind)
			assert.Equal(t, tc.status, provider.StatusCode(err))
			assert.Equal(t, tc.retryAfter, provider.RetryAfter(err))
			assert.Equal(t, tc.retryable, provider.IsRetryable(err))
			assert.NotContains(t, err.Error(), "no-such-scenario", "upstream messages are not echoed")

			if tc.status != 0 && tc.status != 200 {
				_, err = c.Stream(ctxT(t), userReq("it-errors", "x"))
				require.ErrorIs(t, err, tc.kind, "stream fails before the first chunk")
			}
		})
	}
}

func TestIntegration_Timeout(t *testing.T) {
	t.Parallel()
	c := newClient(t, container.BaseURL(), scenario(fakellm.ScenarioTimeout, nil),
		openaicompat.WithTimeout(300*time.Millisecond))
	_, err := c.Complete(ctxT(t), userReq("it-timeout", "x"))
	require.ErrorIs(t, err, provider.ErrTimeout)
	assert.True(t, provider.IsRetryable(err))
	_, err = c.Stream(ctxT(t), userReq("it-timeout", "x"))
	require.ErrorIs(t, err, provider.ErrTimeout)
}

func TestIntegration_Embed(t *testing.T) {
	t.Parallel()
	c := newClient(t, container.BaseURL())
	resp, err := c.Embed(ctxT(t), provider.EmbedRequest{
		Model: "it-embed", Input: []string{"a b", "c"}, Dimensions: 8,
	})
	require.NoError(t, err)
	assert.Equal(t, "it-embed", resp.Model)
	require.Len(t, resp.Vectors, 2)
	assert.Len(t, resp.Vectors[0], 8)
	assert.NotEqual(t, resp.Vectors[0], resp.Vectors[1])
	assert.Equal(t, 3, resp.Usage.PromptTokens)

	vecs, err := c.Embedder().Embed(ctxT(t), "it-embed", 8, []string{"a b"})
	require.NoError(t, err)
	assert.Equal(t, resp.Vectors[:1], vecs, "embeddings are deterministic")

	recs := containerRequests(t, "it-embed")
	require.Len(t, recs, 2)
	assert.Equal(t, "/v1/embeddings", recs[0].Path)
	assert.Equal(t, fakellm.KindEmbeddings, recs[0].Kind)
	assert.Equal(t, 2, recs[0].InputCount)

	_, err = c.Embedder().Embed(ctxT(t), "it-embed", 99999, []string{"x"})
	require.ErrorIs(t, err, provider.ErrBadRequest)
}

func TestIntegration_Models(t *testing.T) {
	t.Parallel()
	c := newClient(t, container.BaseURL())
	_, err := c.Complete(ctxT(t), userReq("it-models-seen", "x"))
	require.NoError(t, err)
	names, err := c.Models(ctxT(t))
	require.NoError(t, err)
	assert.Contains(t, names, fakellm.DefaultModel)
	assert.Contains(t, names, "it-models-seen")
}
