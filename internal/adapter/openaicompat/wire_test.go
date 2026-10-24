// SPDX-License-Identifier: Apache-2.0

package openaicompat_test

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/openaicompat"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

const okBody = `{"id":"x","model":"m","created":1,` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`

type captureServer struct {
	mu     sync.Mutex
	bodies [][]byte
	header []http.Header
}

func (c *captureServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	c.header = append(c.header, r.Header.Clone())
	c.mu.Unlock()
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, okBody)
}

func (c *captureServer) last(t *testing.T) ([]byte, http.Header) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.bodies)
	return c.bodies[len(c.bodies)-1], c.header[len(c.header)-1]
}

func requestShapes() []struct {
	name   string
	req    chat.Request
	stream bool
} {
	params := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
	return []struct {
		name   string
		req    chat.Request
		stream bool
	}{
		{name: "simple_user", req: userReq("gpt-x", "hello")},
		{name: "system_multi_turn", req: chat.Request{Model: "gpt-x", Messages: []chat.Message{
			chat.TextMessage(chat.RoleSystem, "be brief"),
			chat.TextMessage(chat.RoleUser, "hi"),
			chat.TextMessage(chat.RoleAssistant, "hello"),
			chat.TextMessage(chat.RoleUser, "how are you?"),
		}}},
		{name: "params", req: chat.Request{
			Model: "gpt-x", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "x")},
			Temperature: ptr(0.0), TopP: ptr(0.9), MaxTokens: ptr(10), MaxCompletionTokens: ptr(20),
			Stop: []string{"END", "\n\n"}, Seed: ptr(int64(7)), User: "u-1",
			ResponseFormat: json.RawMessage(`{"type":"json_object"}`),
		}},
		{name: "tools", req: chat.Request{
			Model: "gpt-x", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "weather in Hanoi?")},
			Tools: []chat.Tool{{Type: "function", Function: chat.ToolFunction{
				Name: "get_weather", Description: "Current weather", Parameters: params, Strict: ptr(true),
			}}, {Function: chat.ToolFunction{Name: "noop"}}},
			ToolChoice: json.RawMessage(`"auto"`),
		}},
		{name: "tool_round_trip", req: chat.Request{Model: "gpt-x", Messages: []chat.Message{
			chat.TextMessage(chat.RoleUser, "weather in Hanoi?"),
			{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{
				ID: "call_1", Function: chat.FunctionCall{Name: "get_weather", Arguments: `{"city":"Hanoi"}`},
			}}},
			{Role: chat.RoleTool, ToolCallID: "call_1", Content: []chat.Part{{Type: chat.PartText, Text: "31C"}}},
		}}},
		{name: "image_parts", req: chat.Request{Model: "gpt-x", Messages: []chat.Message{{
			Role: chat.RoleUser, Name: "alice",
			Content: []chat.Part{
				{Type: chat.PartText, Text: "what is this?"},
				{Type: chat.PartImage, ImageURL: "https://example.com/a.png", ImageDetail: "low"},
			},
		}}}},
		{name: "text_parts_array", req: chat.Request{Model: "gpt-x", Messages: []chat.Message{{
			Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: "a"}, {Type: chat.PartText, Text: "b"}},
		}}}},
		{name: "empty_content", req: chat.Request{Model: "gpt-x", Messages: []chat.Message{{Role: chat.RoleUser}}}},
		{name: "extra_fields", req: chat.Request{
			Model: "gpt-x", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "x")},
			Extra: map[string]json.RawMessage{
				"logprobs": json.RawMessage(`true`), "model": json.RawMessage(`"evil"`),
				"temperature": json.RawMessage(`2`), "n": json.RawMessage(`1`),
			},
		}},
		{name: "stream_usage", stream: true, req: chat.Request{
			Model: "gpt-x", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "x")}, IncludeUsage: true,
		}},
		{name: "stream_no_usage", stream: true, req: userReq("gpt-x", "x")},
	}
}

func TestClient_Complete_RequestGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range requestShapes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cs := &captureServer{}
			c := newClient(t, newServer(t, cs.handle))
			if tc.stream {
				s, err := c.Stream(ctxT(t), tc.req)
				require.NoError(t, err)
				require.NoError(t, s.Close())
			} else {
				_, err := c.Complete(ctxT(t), tc.req)
				require.NoError(t, err)
			}
			body, _ := cs.last(t)
			golden(t, "request_"+tc.name, body)
		})
	}
}

func TestClient_Complete_UnknownPartType(t *testing.T) {
	t.Parallel()
	c := newClient(t, newServer(t, (&captureServer{}).handle))
	req := chat.Request{Model: "m", Messages: []chat.Message{{
		Role: chat.RoleUser, Content: []chat.Part{{Type: "audio"}},
	}}}
	_, err := c.Complete(ctxT(t), req)
	require.ErrorIs(t, err, provider.ErrBadRequest)
	_, err = c.Stream(ctxT(t), req)
	require.ErrorIs(t, err, provider.ErrBadRequest)
}

func TestClient_Complete_Headers(t *testing.T) {
	t.Parallel()
	cs := &captureServer{}
	c := newClient(t, newServer(t, cs.handle),
		openaicompat.WithAPIKey("sk-test"),
		openaicompat.WithExtraHeaders(map[string]string{"X-Org": "a", "X-Shared": "provider"}),
		openaicompat.WithModelHeaders("gpt-x", map[string]string{"X-Shared": "target"}),
	)
	_, err := c.Complete(ctxT(t), userReq("gpt-x", "x"))
	require.NoError(t, err)
	_, h := cs.last(t)
	assert.Equal(t, "Bearer sk-test", h.Get("Authorization"))
	assert.Equal(t, "a", h.Get("X-Org"))
	assert.Equal(t, "target", h.Get("X-Shared"))
	assert.Equal(t, "application/json", h.Get("Content-Type"))

	_, err = c.Complete(ctxT(t), userReq("other", "x"))
	require.NoError(t, err)
	_, h = cs.last(t)
	assert.Equal(t, "provider", h.Get("X-Shared"))
}

func TestClient_Complete_NoAPIKey(t *testing.T) {
	t.Parallel()
	cs := &captureServer{}
	c := newClient(t, newServer(t, cs.handle))
	_, err := c.Complete(ctxT(t), userReq("m", "x"))
	require.NoError(t, err)
	_, h := cs.last(t)
	assert.Empty(t, h.Get("Authorization"))
}
