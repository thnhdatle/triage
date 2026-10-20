// SPDX-License-Identifier: Apache-2.0

package chat_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func imagePart() chat.Part { return chat.Part{Type: chat.PartImage, ImageURL: "u"} }

func textPart(s string) chat.Part { return chat.Part{Type: chat.PartText, Text: s} }

func TestTextMessage_New_SinglePlainPart(t *testing.T) {
	t.Parallel()
	m := chat.TextMessage(chat.RoleUser, "hi")
	assert.Equal(t, chat.RoleUser, m.Role)
	assert.True(t, m.ContentIsString)
	assert.Equal(t, []chat.Part{textPart("hi")}, m.Content)
}

func TestMessage_Text_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		m    chat.Message
		want string
	}{
		{name: "no content", m: chat.Message{}, want: ""},
		{name: "single text", m: chat.TextMessage(chat.RoleUser, "hello"), want: "hello"},
		{name: "single image", m: chat.Message{Content: []chat.Part{imagePart()}}, want: ""},
		{
			name: "multi text concatenated",
			m:    chat.Message{Content: []chat.Part{textPart("a"), textPart("b"), textPart("c")}},
			want: "abc",
		},
		{
			name: "images skipped",
			m:    chat.Message{Content: []chat.Part{textPart("a"), imagePart(), textPart("b")}},
			want: "ab",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.m.Text())
		})
	}
}

func TestMessage_HasImage_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		m    chat.Message
		want bool
	}{
		{name: "empty", m: chat.Message{}, want: false},
		{name: "text only", m: chat.TextMessage(chat.RoleUser, "x"), want: false},
		{name: "has image", m: chat.Message{Content: []chat.Part{textPart("x"), imagePart()}}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.m.HasImage())
		})
	}
}

func TestRequest_LastUserIndex_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msgs []chat.Message
		want int
	}{
		{name: "no messages", msgs: nil, want: -1},
		{name: "no user message", msgs: []chat.Message{chat.TextMessage(chat.RoleSystem, "s")}, want: -1},
		{
			name: "last user not last message",
			msgs: []chat.Message{
				chat.TextMessage(chat.RoleUser, "u1"),
				chat.TextMessage(chat.RoleUser, "u2"),
				chat.TextMessage(chat.RoleAssistant, "a"),
			},
			want: 1,
		},
		{name: "single user", msgs: []chat.Message{chat.TextMessage(chat.RoleUser, "u")}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, chat.Request{Messages: tc.msgs}.LastUserIndex())
		})
	}
}

func TestRequest_LastUserText_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msgs []chat.Message
		want string
	}{
		{name: "no user message", msgs: []chat.Message{chat.TextMessage(chat.RoleSystem, "s")}, want: ""},
		{
			name: "picks last user",
			msgs: []chat.Message{
				chat.TextMessage(chat.RoleUser, "first"),
				chat.TextMessage(chat.RoleAssistant, "a"),
				chat.TextMessage(chat.RoleUser, "second"),
			},
			want: "second",
		},
		{
			name: "multi part",
			msgs: []chat.Message{{Role: chat.RoleUser, Content: []chat.Part{textPart("x"), imagePart(), textPart("y")}}},
			want: "xy",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, chat.Request{Messages: tc.msgs}.LastUserText())
		})
	}
}

func TestRequest_HasImage_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msgs []chat.Message
		want bool
	}{
		{name: "none", msgs: nil, want: false},
		{name: "text", msgs: []chat.Message{chat.TextMessage(chat.RoleUser, "x")}, want: false},
		{
			name: "image in second message",
			msgs: []chat.Message{
				chat.TextMessage(chat.RoleUser, "x"),
				{Role: chat.RoleUser, Content: []chat.Part{imagePart()}},
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, chat.Request{Messages: tc.msgs}.HasImage())
		})
	}
}

func TestRequest_RequestedOutputTokens_Table(t *testing.T) {
	t.Parallel()
	ptr := func(n int) *int { return &n }
	tests := []struct {
		name string
		req  chat.Request
		want int
	}{
		{name: "neither", req: chat.Request{}, want: 0},
		{name: "max tokens only", req: chat.Request{MaxTokens: ptr(100)}, want: 100},
		{name: "completion only", req: chat.Request{MaxCompletionTokens: ptr(200)}, want: 200},
		{name: "completion wins", req: chat.Request{MaxTokens: ptr(100), MaxCompletionTokens: ptr(200)}, want: 200},
		{name: "explicit zero completion wins", req: chat.Request{MaxTokens: ptr(100), MaxCompletionTokens: ptr(0)}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.req.RequestedOutputTokens())
		})
	}
}

func cloneFixture() chat.Request {
	return chat.Request{
		ID:    "r1",
		Model: "m",
		Messages: []chat.Message{
			{
				Role:    chat.RoleAssistant,
				Content: []chat.Part{textPart("orig")},
				ToolCalls: []chat.ToolCall{
					{ID: "t1", Type: "function", Function: chat.FunctionCall{Name: "f", Arguments: "{}"}},
				},
			},
			chat.TextMessage(chat.RoleUser, "q"),
		},
		Tools:   []chat.Tool{{Type: "function", Function: chat.ToolFunction{Name: "f"}}},
		Stop:    []string{"END"},
		Extra:   map[string]json.RawMessage{"k": json.RawMessage(`1`)},
		Headers: map[string]string{"X-Triage-Route": "auto"},
	}
}

func TestRequest_Clone_Independent(t *testing.T) {
	t.Parallel()
	orig := cloneFixture()
	want := cloneFixture()
	c := orig.Clone()
	require.Equal(t, want, c)

	c.Messages[0].Content[0].Text = "changed"
	c.Messages[0].ToolCalls[0].ID = "changed"
	c.Messages[0] = chat.TextMessage(chat.RoleSystem, "replaced")
	c.Messages[1].Content[0].Text = "changed"
	c.Tools[0].Function.Name = "changed"
	c.Stop[0] = "changed"
	c.Extra["k"] = json.RawMessage(`2`)
	c.Extra["new"] = json.RawMessage(`3`)
	c.Headers["X-Triage-Route"] = "changed"
	c.Headers["New"] = "x"

	assert.Equal(t, want, orig)
}

func TestRequest_Clone_NilCollectionsStayNil(t *testing.T) {
	t.Parallel()
	c := chat.Request{}.Clone()
	assert.Nil(t, c.Extra)
	assert.Nil(t, c.Headers)
	assert.Empty(t, c.Messages)
	assert.Empty(t, c.Tools)
	assert.Empty(t, c.Stop)
}
