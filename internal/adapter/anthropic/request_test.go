// SPDX-License-Identifier: Apache-2.0

package anthropic_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/anthropic"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

const weatherSchema = `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`

func weatherTool() chat.Tool {
	return chat.Tool{Type: "function", Function: chat.ToolFunction{
		Name: "get_weather", Description: "Current weather", Parameters: json.RawMessage(weatherSchema),
	}}
}

func withMessages(msgs ...chat.Message) chat.Request {
	return chat.Request{Model: "claude-haiku-5-5", Messages: msgs}
}

func text(role chat.Role, s string) chat.Message { return chat.TextMessage(role, s) }

func toolCallMsg(id, name, args string) chat.Message {
	return chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{
		ID: id, Type: "function", Function: chat.FunctionCall{Name: name, Arguments: args},
	}}}
}

func toolResult(id, content string) chat.Message {
	m := text(chat.RoleTool, content)
	m.ToolCallID = id
	return m
}

type requestShape struct {
	name   string
	req    chat.Request
	stream bool
}

func requestShapes() []requestShape {
	tools := func(r chat.Request, choice string) chat.Request {
		r.Tools = []chat.Tool{weatherTool()}
		if choice != "" {
			r.ToolChoice = json.RawMessage(choice)
		}
		return r
	}
	weather := withMessages(text(chat.RoleUser, "Weather in Hanoi?"))
	return []requestShape{
		{name: "simple_user", req: userReq("hello")},
		{name: "system_prompt", req: withMessages(text(chat.RoleSystem, "Be brief."), text(chat.RoleUser, "hi"))},
		{name: "multiple_system", req: withMessages(
			text(chat.RoleSystem, "Rule one."), text(chat.RoleUser, "hi"), text(chat.RoleSystem, "Rule two."),
		)},
		{name: "multi_turn", req: withMessages(
			text(chat.RoleUser, "hi"), text(chat.RoleAssistant, "hello"), text(chat.RoleUser, "how are you?"),
		)},
		{name: "consecutive_user_merged", req: withMessages(
			text(chat.RoleUser, "part one"), text(chat.RoleUser, "part two"),
		)},
		{name: "max_completion_tokens", req: func() chat.Request {
			r := userReq("x")
			r.MaxTokens, r.MaxCompletionTokens = ptr(100), ptr(200)
			return r
		}()},
		{name: "max_tokens", req: func() chat.Request { r := userReq("x"); r.MaxTokens = ptr(50); return r }()},
		{name: "sampling_stop_user", req: func() chat.Request {
			r := userReq("count")
			r.Temperature, r.TopP, r.Stop = ptr(0.2), ptr(0.9), []string{"five", "\n\n"}
			r.User, r.Seed = "user-42", ptr(int64(7))
			r.Extra = map[string]json.RawMessage{"logprobs": json.RawMessage(`true`)}
			return r
		}()},
		{name: "tools_auto", req: tools(weather, `"auto"`)},
		{name: "tools_no_choice", req: tools(weather, "")},
		{name: "tools_required", req: tools(weather, `"required"`)},
		{name: "tools_none", req: tools(weather, `"none"`)},
		{name: "tools_named", req: tools(weather, `{"type":"function","function":{"name":"get_weather"}}`)},
		{name: "tools_no_parallel", req: func() chat.Request {
			r := tools(weather, `"auto"`)
			r.Extra = map[string]json.RawMessage{"parallel_tool_calls": json.RawMessage(`false`)}
			return r
		}()},
		{name: "no_parallel_without_choice", req: func() chat.Request {
			r := tools(weather, "")
			r.Extra = map[string]json.RawMessage{"parallel_tool_calls": json.RawMessage(`false`)}
			return r
		}()},
		{name: "tool_without_parameters_strict", req: func() chat.Request {
			r := userReq("time?")
			r.Tools = []chat.Tool{{Function: chat.ToolFunction{Name: "get_time", Strict: ptr(true)}}}
			return r
		}()},
		{name: "tool_round_trip", req: tools(withMessages(
			text(chat.RoleUser, "Weather in Hanoi and Hue?"),
			chat.Message{
				Role: chat.RoleAssistant, Content: []chat.Part{{Type: chat.PartText, Text: "Checking."}},
				ToolCalls: []chat.ToolCall{
					{ID: "toolu_1", Function: chat.FunctionCall{Name: "get_weather", Arguments: `{"city":"Hanoi"}`}},
					{ID: "toolu_2", Function: chat.FunctionCall{Name: "get_weather", Arguments: ` {"city": "Hue"} `}},
				},
			},
			toolResult("toolu_1", "31C"),
			toolResult("toolu_2", "28C"),
			text(chat.RoleUser, "Which is warmer?"),
		), "")},
		{name: "tool_call_empty_arguments", req: withMessages(
			text(chat.RoleUser, "time?"), toolCallMsg("toolu_9", "get_time", ""), toolResult("toolu_9", "12:00"),
		)},
		{name: "image_url_and_data", req: withMessages(chat.Message{Role: chat.RoleUser, Content: []chat.Part{
			{Type: chat.PartText, Text: "Compare these."},
			{Type: chat.PartImage, ImageURL: "https://example.com/a.png", ImageDetail: "high"},
			{Type: chat.PartImage, ImageURL: "data:image/png;base64,iVBORw0KGgo="},
		}})},
		{name: "empty_text_parts_dropped", req: withMessages(chat.Message{Role: chat.RoleUser, Content: []chat.Part{
			{Type: chat.PartText, Text: ""}, {Type: chat.PartText, Text: "kept"},
		}})},
		{name: "response_format_text", req: func() chat.Request {
			r := userReq("x")
			r.ResponseFormat = json.RawMessage(`{"type":"text"}`)
			return r
		}()},
		{name: "continuation", req: withMessages(
			text(chat.RoleUser, "Write a haiku."), text(chat.RoleAssistant, "Autumn moonlight—"),
			text(chat.RoleUser, "Continue exactly where you stopped."),
		)},
		{name: "stream", stream: true, req: func() chat.Request { r := userReq("x"); r.IncludeUsage = true; return r }()},
	}
}

func TestClient_Complete_RequestGolden(t *testing.T) {
	t.Parallel()
	shapes := requestShapes()
	require.GreaterOrEqual(t, len(shapes), 15, "at least 15 request shapes")
	for _, tc := range shapes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := newFixtureServer(t)
			if tc.stream {
				s, err := fs.client(t, "stream_text").Stream(ctxT(t), tc.req)
				require.NoError(t, err)
				require.NoError(t, s.Close())
			} else {
				_, err := fs.client(t, "text").Complete(ctxT(t), tc.req)
				require.NoError(t, err)
			}
			golden(t, "request_"+tc.name, fs.last(t).body)
		})
	}
}

func TestClient_Complete_RequestHeaders(t *testing.T) {
	t.Parallel()
	fs := newFixtureServer(t)
	c := fs.client(t, "text",
		anthropic.WithExtraHeaders(map[string]string{"Anthropic-Beta": "x-2026", "X-Shared": "provider"}),
		anthropic.WithModelHeaders("claude-haiku-5-5", map[string]string{"X-Shared": "target"}),
		anthropic.WithVersion("2099-01-01"),
	)
	_, err := c.Complete(ctxT(t), userReq("x"))
	require.NoError(t, err)
	r := fs.last(t)
	assert.Equal(t, "POST", r.method)
	assert.Equal(t, "/v1/messages", r.path)
	assert.Equal(t, "sk-ant-test", r.header.Get("X-Api-Key"))
	assert.Equal(t, "2099-01-01", r.header.Get("Anthropic-Version"))
	assert.Equal(t, "x-2026", r.header.Get("Anthropic-Beta"))
	assert.Equal(t, "target", r.header.Get("X-Shared"))
	assert.Empty(t, r.header.Get("Authorization"))
}

func TestClient_Complete_DefaultHeaders(t *testing.T) {
	t.Parallel()
	fs := newFixtureServer(t)
	c := newClient(t, anthropic.WithBaseURL(fs.url+"/"), anthropic.WithDefaultMaxTokens(77),
		anthropic.WithExtraHeaders(map[string]string{fixtureHeader: "text"}))
	_, err := c.Complete(ctxT(t), userReq("x"))
	require.NoError(t, err)
	r := fs.last(t)
	assert.Equal(t, anthropic.DefaultVersion, r.header.Get("Anthropic-Version"))
	assert.Empty(t, r.header.Get("X-Api-Key"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(r.body, &body))
	assert.InDelta(t, 77, body["max_tokens"], 0)
}

func TestClient_Complete_RequestRejected_Table(t *testing.T) {
	t.Parallel()
	img := func(u string) chat.Request {
		return withMessages(chat.Message{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartImage, ImageURL: u}}})
	}
	tests := []struct {
		name string
		req  chat.Request
	}{
		{name: "json response format", req: func() chat.Request {
			r := userReq("x")
			r.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
			return r
		}()},
		{name: "invalid response format", req: func() chat.Request {
			r := userReq("x")
			r.ResponseFormat = json.RawMessage(`[`)
			return r
		}()},
		{name: "image in system", req: withMessages(chat.Message{Role: chat.RoleSystem, Content: []chat.Part{
			{Type: chat.PartImage, ImageURL: "https://example.com/a.png"},
		}})},
		{name: "image ftp url", req: img("ftp://example.com/a.png")},
		{name: "image data not base64", req: img("data:image/png,abc")},
		{name: "image data empty", req: img("data:image/png;base64,")},
		{name: "image media type", req: img("data:image/tiff;base64,AAAA")},
		{name: "unknown part", req: withMessages(chat.Message{Role: chat.RoleUser, Content: []chat.Part{{Type: "audio"}}})},
		{name: "unknown role", req: withMessages(text("developer", "x"))},
		{name: "empty message", req: withMessages(text(chat.RoleUser, ""))},
		{name: "tool args not object", req: withMessages(text(chat.RoleUser, "x"), toolCallMsg("t", "f", `[1]`))},
		{name: "tool args invalid", req: withMessages(text(chat.RoleUser, "x"), toolCallMsg("t", "f", `{"a":`))},
		{name: "tool type", req: func() chat.Request {
			r := userReq("x")
			r.Tools = []chat.Tool{{Type: "retrieval", Function: chat.ToolFunction{Name: "r"}}}
			return r
		}()},
		{name: "tool choice string", req: func() chat.Request {
			r := userReq("x")
			r.ToolChoice = json.RawMessage(`"sometimes"`)
			return r
		}()},
		{name: "tool choice object", req: func() chat.Request {
			r := userReq("x")
			r.ToolChoice = json.RawMessage(`{"type":"function","function":{}}`)
			return r
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := newFixtureServer(t)
			c := fs.client(t, "text")
			_, err := c.Complete(ctxT(t), tc.req)
			require.ErrorIs(t, err, provider.ErrBadRequest)
			assert.Contains(t, err.Error(), "anthropic: ")
			_, err = c.Stream(ctxT(t), tc.req)
			require.ErrorIs(t, err, provider.ErrBadRequest)
			fs.mu.Lock()
			defer fs.mu.Unlock()
			assert.Empty(t, fs.reqs, "nothing is sent")
		})
	}
}
