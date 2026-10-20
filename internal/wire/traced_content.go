// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"encoding/json"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// Content attribute keys (OTel GenAI semantic conventions), set only under
// tracing.capture_content.
const (
	attrInputMessages  = "gen_ai.input.messages"
	attrOutputMessages = "gen_ai.output.messages"
)

type contentPart struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type contentMessage struct {
	Role         string        `json:"role"`
	Parts        []contentPart `json:"parts"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

func partsOf(m chat.Message) []contentPart {
	parts := make([]contentPart, 0, len(m.Content)+len(m.ToolCalls))
	for _, p := range m.Content {
		if p.Type == chat.PartText {
			parts = append(parts, contentPart{Type: "text", Content: p.Text})
		} else {
			parts = append(parts, contentPart{Type: "image"}) // the URL may be a data: payload; never recorded
		}
	}
	for _, c := range m.ToolCalls {
		parts = append(parts, contentPart{
			Type: "tool_call", ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments,
		})
	}
	return parts
}

// inputMessages is the JSON of the messages exactly as sent upstream.
func inputMessages(req chat.Request) string {
	out := make([]contentMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		out = append(out, contentMessage{Role: string(m.Role), Parts: partsOf(m)})
	}
	return marshalContent(out)
}

// outputMessages is the JSON of the choices exactly as received upstream.
func outputMessages(resp chat.Response) string {
	out := make([]contentMessage, 0, len(resp.Choices))
	for _, c := range resp.Choices {
		out = append(out, contentMessage{
			Role: string(c.Message.Role), Parts: partsOf(c.Message), FinishReason: string(c.FinishReason),
		})
	}
	return marshalContent(out)
}

func marshalContent(v []contentMessage) string {
	b, err := json.Marshal(v)
	if err != nil { // plain strings cannot fail to marshal
		return "[]"
	}
	return string(b)
}

// streamContent accumulates the text and finish reason of the first choice of
// a stream for gen_ai.output.messages.
type streamContent struct {
	text   strings.Builder
	finish chat.FinishReason
	calls  map[int]*contentPart
}

func (s *streamContent) add(c chat.Chunk) {
	for _, ch := range c.Choices {
		if ch.Index != 0 {
			continue
		}
		s.text.WriteString(ch.Delta.Content)
		for _, tc := range ch.Delta.ToolCalls {
			if s.calls == nil {
				s.calls = map[int]*contentPart{}
			}
			p := s.calls[tc.Index]
			if p == nil {
				p = &contentPart{Type: "tool_call"}
				s.calls[tc.Index] = p
			}
			if tc.ID != "" {
				p.ID = tc.ID
			}
			if tc.Name != "" {
				p.Name = tc.Name
			}
			p.Arguments += tc.Arguments
		}
		if ch.FinishReason != "" {
			s.finish = ch.FinishReason
		}
	}
}

func (s *streamContent) set(span trace.Span) {
	m := chat.Message{Role: chat.RoleAssistant}
	if s.text.Len() > 0 {
		m.Content = []chat.Part{{Type: chat.PartText, Text: s.text.String()}}
	}
	for i := range len(s.calls) {
		if p := s.calls[i]; p != nil {
			m.ToolCalls = append(m.ToolCalls, chat.ToolCall{
				ID: p.ID, Type: "function",
				Function: chat.FunctionCall{Name: p.Name, Arguments: p.Arguments},
			})
		}
	}
	span.SetAttributes(attribute.String(attrOutputMessages, marshalContent([]contentMessage{
		{Role: "assistant", Parts: partsOf(m), FinishReason: string(s.finish)},
	})))
}
