// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"encoding/json"
	"strings"
)

// Role is the author of a message.
type Role string

// Message roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// PartType is the kind of a content part.
type PartType string

// Content part kinds.
const (
	PartText  PartType = "text"
	PartImage PartType = "image_url"
)

// Part is one element of a message's content.
type Part struct {
	Type PartType
	// Text is set for PartText.
	Text string
	// ImageURL is set for PartImage; images are never scanned or scored.
	ImageURL string
	// ImageDetail is the optional OpenAI image detail ("auto", "low", "high").
	ImageDetail string
}

// FunctionCall is the function name and JSON-encoded arguments of a tool call.
type FunctionCall struct {
	Name string
	// Arguments is a JSON document encoded as a string, as produced by the model.
	Arguments string
}

// ToolCall is a call the assistant asks the client to execute.
type ToolCall struct {
	ID       string
	Type     string
	Function FunctionCall
}

// Message is one turn of a conversation.
type Message struct {
	Role Role
	// Content holds the content parts. A plain string content is one PartText.
	Content []Part
	// ContentIsString records that the client sent content as a plain string, so
	// adapters can serialise it back in the same shape.
	ContentIsString bool
	Name            string
	ToolCalls       []ToolCall
	ToolCallID      string
}

// TextMessage returns a message with a single plain-string text part.
func TextMessage(role Role, text string) Message {
	return Message{Role: role, Content: []Part{{Type: PartText, Text: text}}, ContentIsString: true}
}

// Text returns the concatenation of the message's text parts, in order.
func (m Message) Text() string {
	if len(m.Content) == 1 && m.Content[0].Type == PartText {
		return m.Content[0].Text
	}
	var b strings.Builder
	for _, p := range m.Content {
		if p.Type == PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// HasImage reports whether the message has an image part.
func (m Message) HasImage() bool {
	for _, p := range m.Content {
		if p.Type == PartImage {
			return true
		}
	}
	return false
}

// ToolFunction describes a function the model may call.
type ToolFunction struct {
	Name        string
	Description string
	// Parameters is the JSON Schema of the arguments, passed through unchanged.
	Parameters json.RawMessage
	Strict     *bool
}

// Tool is a tool offered to the model.
type Tool struct {
	Type     string
	Function ToolFunction
}

// Request is a provider-neutral chat completion request.
type Request struct {
	// ID is the router's request id (also used to seed the random strategy).
	ID string
	// Model is the route name on input and the target's model name on an attempt.
	Model    string
	Messages []Message
	Tools    []Tool
	// ToolChoice, ResponseFormat are passed through as raw JSON.
	ToolChoice          json.RawMessage
	ResponseFormat      json.RawMessage
	Temperature         *float64
	TopP                *float64
	MaxTokens           *int
	MaxCompletionTokens *int
	Stop                []string
	Seed                *int64
	User                string
	Stream              bool
	IncludeUsage        bool
	// Extra holds unknown top-level request fields, passed through to
	// OpenAI-compatible providers unchanged.
	Extra map[string]json.RawMessage
	// Headers holds client request headers relevant to routing (X-Triage-*),
	// keyed by canonical header name.
	Headers map[string]string
}

// LastUserIndex returns the index of the last user message, or -1.
func (r Request) LastUserIndex() int {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == RoleUser {
			return i
		}
	}
	return -1
}

// LastUserText returns the text parts of the last user message, or "" when
// there is no user message.
func (r Request) LastUserText() string {
	i := r.LastUserIndex()
	if i < 0 {
		return ""
	}
	return r.Messages[i].Text()
}

// HasImage reports whether any message has an image part.
func (r Request) HasImage() bool {
	for _, m := range r.Messages {
		if m.HasImage() {
			return true
		}
	}
	return false
}

// RequestedOutputTokens returns max_completion_tokens, else max_tokens, else 0.
func (r Request) RequestedOutputTokens() int {
	if r.MaxCompletionTokens != nil {
		return *r.MaxCompletionTokens
	}
	if r.MaxTokens != nil {
		return *r.MaxTokens
	}
	return 0
}

// Clone returns a deep copy of the request, so a transform of one attempt
// never aliases another attempt's request.
func (r Request) Clone() Request {
	c := r
	c.Messages = make([]Message, len(r.Messages))
	for i, m := range r.Messages {
		c.Messages[i] = m.clone()
	}
	c.Tools = append([]Tool(nil), r.Tools...)
	c.Stop = append([]string(nil), r.Stop...)
	if r.Extra != nil {
		c.Extra = make(map[string]json.RawMessage, len(r.Extra))
		for k, v := range r.Extra {
			c.Extra[k] = v
		}
	}
	if r.Headers != nil {
		c.Headers = make(map[string]string, len(r.Headers))
		for k, v := range r.Headers {
			c.Headers[k] = v
		}
	}
	return c
}

func (m Message) clone() Message {
	c := m
	c.Content = append([]Part(nil), m.Content...)
	c.ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
	return c
}
