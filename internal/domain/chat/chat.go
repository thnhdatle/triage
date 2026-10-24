// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"encoding/json"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type PartType string

const (
	PartText  PartType = "text"
	PartImage PartType = "image_url"
)

type Part struct {
	Type        PartType
	Text        string
	ImageURL    string
	ImageDetail string
}

type FunctionCall struct {
	Name      string
	Arguments string
}

type ToolCall struct {
	ID       string
	Type     string
	Function FunctionCall
}

// Message is one chat message.
type Message struct {
	Role            Role
	Content         []Part
	ContentIsString bool
	Name            string
	ToolCalls       []ToolCall
	ToolCallID      string
}

func TextMessage(role Role, text string) Message {
	return Message{Role: role, Content: []Part{{Type: PartText, Text: text}}, ContentIsString: true}
}

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

func (m Message) HasImage() bool {
	for _, p := range m.Content {
		if p.Type == PartImage {
			return true
		}
	}
	return false
}

type ToolFunction struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      *bool
}

type Tool struct {
	Type     string
	Function ToolFunction
}

// Request is a provider-neutral chat request.
type Request struct {
	ID                  string
	Model               string
	Messages            []Message
	Tools               []Tool
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
	Extra               map[string]json.RawMessage
	Headers             map[string]string
}

func (r Request) LastUserIndex() int {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == RoleUser {
			return i
		}
	}
	return -1
}

func (r Request) LastUserText() string {
	i := r.LastUserIndex()
	if i < 0 {
		return ""
	}
	return r.Messages[i].Text()
}

func (r Request) HasImage() bool {
	for _, m := range r.Messages {
		if m.HasImage() {
			return true
		}
	}
	return false
}

func (r Request) RequestedOutputTokens() int {
	if r.MaxCompletionTokens != nil {
		return *r.MaxCompletionTokens
	}
	if r.MaxTokens != nil {
		return *r.MaxTokens
	}
	return 0
}

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
