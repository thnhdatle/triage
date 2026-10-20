// SPDX-License-Identifier: Apache-2.0

package chat_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

type fixedCounter struct{ n int }

func (f fixedCounter) CountTokens(string) int { return f.n }

func TestEstimateTokens_Count_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "empty", text: "", want: 0},
		{name: "one ascii byte", text: "a", want: 1},
		{name: "four ascii bytes", text: "abcd", want: 1},
		{name: "five ascii bytes rounds up", text: "abcde", want: 2},
		{name: "eight ascii bytes", text: "abcdefgh", want: 2},
		{name: "korean one syllable is three bytes", text: "안", want: 1},
		{name: "korean two syllables", text: "안녕", want: 2},
		{name: "korean four syllables", text: "안녕하세", want: 4},
		{name: "emoji is four bytes", text: "😀", want: 2},
		{name: "mixed ascii and korean", text: "a안", want: 2},
		{name: "mixed 4 ascii and 3 non ascii", text: "abcd안", want: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, chat.EstimateTokens(tc.text))
		})
	}
}

func TestEstimator_CountTokens_MatchesEstimate(t *testing.T) {
	t.Parallel()
	var c chat.TokenCounter = chat.Estimator{}
	assert.Equal(t, chat.EstimateTokens("hello 안녕"), c.CountTokens("hello 안녕"))
}

func TestEstimateTokens_Concat_Subadditive(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		a := rapid.String().Draw(rt, "a")
		b := rapid.String().Draw(rt, "b")
		got := chat.EstimateTokens(a + b)
		if got > chat.EstimateTokens(a)+chat.EstimateTokens(b)+1 {
			rt.Fatalf("count(a+b)=%d exceeds count(a)+count(b)+1", got)
		}
		if got < 0 {
			rt.Fatalf("negative count %d", got)
		}
	})
}

func TestRequest_PromptText_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  chat.Request
		want string
	}{
		{name: "no messages", req: chat.Request{}, want: ""},
		{
			name: "text messages newline terminated",
			req: chat.Request{Messages: []chat.Message{
				chat.TextMessage(chat.RoleSystem, "sys"),
				chat.TextMessage(chat.RoleUser, "hi"),
			}},
			want: "sys\nhi\n",
		},
		{
			name: "image parts skipped",
			req: chat.Request{Messages: []chat.Message{{
				Role: chat.RoleUser,
				Content: []chat.Part{
					{Type: chat.PartText, Text: "look"},
					{Type: chat.PartImage, ImageURL: "http://x/y.png"},
				},
			}}},
			want: "look\n",
		},
		{
			name: "tool call arguments included",
			req: chat.Request{Messages: []chat.Message{{
				Role: chat.RoleAssistant,
				ToolCalls: []chat.ToolCall{
					{ID: "1", Function: chat.FunctionCall{Name: "f", Arguments: `{"a":1}`}},
					{ID: "2", Function: chat.FunctionCall{Name: "g", Arguments: `{}`}},
				},
			}}},
			want: "{\"a\":1}\n{}\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.req.PromptText())
		})
	}
}

func TestRequest_PromptTokens_Counter(t *testing.T) {
	t.Parallel()
	req := chat.Request{Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "abcdefg")}}
	tests := []struct {
		name    string
		counter chat.TokenCounter
		want    int
	}{
		{name: "nil counter uses estimate", counter: nil, want: chat.EstimateTokens("abcdefg\n")},
		{name: "custom counter", counter: fixedCounter{n: 42}, want: 42},
		{name: "estimator", counter: chat.Estimator{}, want: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, req.PromptTokens(tc.counter))
		})
	}
}
