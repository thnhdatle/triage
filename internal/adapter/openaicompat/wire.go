// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// jsonNull is the JSON null literal.
const jsonNull = "null"

// wireRequest is the OpenAI chat completions request body.
type wireRequest struct {
	Model               string             `json:"model"`
	Messages            []wireMessage      `json:"messages"`
	Tools               []wireTool         `json:"tools,omitempty"`
	ToolChoice          json.RawMessage    `json:"tool_choice,omitempty"`
	ResponseFormat      json.RawMessage    `json:"response_format,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	MaxTokens           *int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int               `json:"max_completion_tokens,omitempty"`
	Stop                []string           `json:"stop,omitempty"`
	Seed                *int64             `json:"seed,omitempty"`
	User                string             `json:"user,omitempty"`
	Stream              bool               `json:"stream,omitempty"`
	StreamOptions       *wireStreamOptions `json:"stream_options,omitempty"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []wireToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type wirePart struct {
	Type     string        `json:"type"`
	Text     *string       `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// encodeRequest builds the request body for req. Unknown fields in
// req.Extra are passed through unless they collide with a known field.
func encodeRequest(req chat.Request, stream bool) ([]byte, error) {
	w := wireRequest{
		Model: req.Model, ToolChoice: req.ToolChoice, ResponseFormat: req.ResponseFormat,
		Temperature: req.Temperature, TopP: req.TopP, MaxTokens: req.MaxTokens,
		MaxCompletionTokens: req.MaxCompletionTokens, Stop: req.Stop, Seed: req.Seed, User: req.User,
		Stream: stream,
	}
	if stream && req.IncludeUsage {
		w.StreamOptions = &wireStreamOptions{IncludeUsage: true}
	}
	w.Messages = make([]wireMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		wm, err := encodeMessage(m)
		if err != nil {
			return nil, err
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, t := range req.Tools {
		typ := t.Type
		if typ == "" {
			typ = "function"
		}
		w.Tools = append(w.Tools, wireTool{Type: typ, Function: wireFunction{
			Name: t.Function.Name, Description: t.Function.Description,
			Parameters: t.Function.Parameters, Strict: t.Function.Strict,
		}})
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: encode request: %w", err)
	}
	if len(req.Extra) == 0 {
		return data, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, fmt.Errorf("openaicompat: encode request: %w", err)
	}
	for k, v := range req.Extra {
		if _, known := merged[k]; !known && !knownField[k] {
			merged[k] = v
		}
	}
	data, err = json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: encode extra fields: %w", err)
	}
	return data, nil
}

// knownField lists request fields the adapter owns even when it omits them,
// so Extra can never override them.
var knownField = map[string]bool{
	"model": true, "messages": true, "tools": true, "tool_choice": true, "response_format": true,
	"temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true,
	"stop": true, "seed": true, "user": true, "stream": true, "stream_options": true,
}

func encodeMessage(m chat.Message) (wireMessage, error) {
	wm := wireMessage{Role: string(m.Role), Name: m.Name, ToolCallID: m.ToolCallID}
	for _, tc := range m.ToolCalls {
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
			ID: tc.ID, Type: typ, Function: wireFunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	content, err := encodeContent(m)
	if err != nil {
		return wireMessage{}, err
	}
	wm.Content = content
	return wm, nil
}

func encodeContent(m chat.Message) (json.RawMessage, error) {
	if len(m.Content) == 0 {
		if len(m.ToolCalls) > 0 {
			return json.RawMessage(jsonNull), nil
		}
		return json.RawMessage(`""`), nil
	}
	if m.ContentIsString && !m.HasImage() {
		return marshalRaw(m.Text())
	}
	parts := make([]wirePart, 0, len(m.Content))
	for _, p := range m.Content {
		switch p.Type {
		case chat.PartText:
			text := p.Text
			parts = append(parts, wirePart{Type: string(chat.PartText), Text: &text})
		case chat.PartImage:
			parts = append(parts, wirePart{
				Type: string(chat.PartImage), ImageURL: &wireImageURL{URL: p.ImageURL, Detail: p.ImageDetail},
			})
		default:
			return nil, badRequest("unsupported content part type " + string(p.Type))
		}
	}
	return marshalRaw(parts)
}

func marshalRaw(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: encode content: %w", err)
	}
	return data, nil
}

// wireUsage is the OpenAI usage object.
type wireUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u *wireUsage) usage() chat.Usage {
	if u == nil {
		return chat.Usage{}
	}
	out := chat.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens}
	if u.PromptTokensDetails != nil {
		out.CachedPromptTokens = u.PromptTokensDetails.CachedTokens
	}
	return out
}

type wireResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []wireToolCall  `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage             *wireUsage `json:"usage"`
	SystemFingerprint string     `json:"system_fingerprint"`
}

// decodeResponse translates a chat completion body.
func decodeResponse(data []byte, status int) (chat.Response, error) {
	var w wireResponse
	if err := json.Unmarshal(data, &w); err != nil {
		return chat.Response{}, malformed(status, "invalid JSON")
	}
	if len(w.Choices) == 0 {
		return chat.Response{}, malformed(status, "no choices")
	}
	resp := chat.Response{
		ID: w.ID, Model: w.Model, Created: w.Created, Usage: w.Usage.usage(), SystemFingerprint: w.SystemFingerprint,
	}
	for _, c := range w.Choices {
		role := chat.Role(c.Message.Role)
		if role == "" {
			role = chat.RoleAssistant
		}
		msg := chat.Message{Role: role, ContentIsString: true}
		text, ok := decodeContent(c.Message.Content)
		if !ok {
			return chat.Response{}, malformed(status, "invalid message content")
		}
		if text != "" || len(c.Message.ToolCalls) == 0 {
			msg.Content = []chat.Part{{Type: chat.PartText, Text: text}}
		}
		for _, tc := range c.Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, chat.ToolCall{
				ID: tc.ID, Type: tc.Type,
				Function: chat.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		resp.Choices = append(resp.Choices, chat.Choice{
			Index: c.Index, Message: msg, FinishReason: chat.FinishReason(c.FinishReason),
		})
	}
	return resp, nil
}

// decodeContent accepts a string, null, or an array of text parts.
func decodeContent(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == jsonNull {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var b bytes.Buffer
	for _, p := range parts {
		if p.Type == string(chat.PartText) {
			b.WriteString(p.Text)
		}
	}
	return b.String(), true
}
