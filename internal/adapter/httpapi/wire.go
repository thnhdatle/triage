// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

var chatKnown = []string{
	paramModel, "messages", "tools", "tool_choice", "response_format", "temperature", "top_p", "max_tokens",
	"max_completion_tokens", "stop", "seed", "user", "stream", "stream_options",
}

type wireChatRequest struct {
	Model               string          `json:"model"`
	Messages            []wireMessage   `json:"messages"`
	Tools               []wireTool      `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	ResponseFormat      json.RawMessage `json:"response_format"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	Stop                json.RawMessage `json:"stop"`
	Seed                *int64          `json:"seed"`
	User                string          `json:"user"`
	Stream              bool            `json:"stream"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name"`
	ToolCalls  []wireToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict"`
	} `json:"function"`
}

type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, *apiError) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, &apiError{
			status: http.StatusRequestEntityTooLarge, code: CodeRequestTooLarge,
			message: fmt.Sprintf("request body exceeds the limit of %d bytes", tooBig.Limit),
		}
	case err != nil:
		return nil, invalid("", "could not read the request body")
	}
	return b, nil
}

func decodeChat(body []byte) (chat.Request, *apiError) {
	var w wireChatRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return chat.Request{}, invalid("", "invalid JSON body: "+err.Error())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return chat.Request{}, invalid("", "invalid JSON body: "+err.Error())
	}
	if w.Model == "" {
		return chat.Request{}, invalid(paramModel, "model is required")
	}
	if len(w.Messages) == 0 {
		return chat.Request{}, invalid("messages", "messages must be a non-empty array")
	}
	req := chat.Request{
		Model: w.Model, Temperature: w.Temperature, TopP: w.TopP, MaxTokens: w.MaxTokens,
		MaxCompletionTokens: w.MaxCompletionTokens, Seed: w.Seed, User: w.User, Stream: w.Stream,
		ToolChoice: nullToNil(w.ToolChoice), ResponseFormat: nullToNil(w.ResponseFormat),
	}
	if w.StreamOptions != nil {
		req.IncludeUsage = w.StreamOptions.IncludeUsage
	}
	var aerr *apiError
	if req.Stop, aerr = decodeStop(w.Stop); aerr != nil {
		return chat.Request{}, aerr
	}
	for i, m := range w.Messages {
		msg, err := decodeMessage(m)
		if err != nil {
			return chat.Request{}, invalid(fmt.Sprintf("messages[%d]", i), err.Error())
		}
		req.Messages = append(req.Messages, msg)
	}
	for _, t := range w.Tools {
		req.Tools = append(req.Tools, chat.Tool{Type: t.Type, Function: chat.ToolFunction{
			Name: t.Function.Name, Description: t.Function.Description,
			Parameters: t.Function.Parameters, Strict: t.Function.Strict,
		}})
	}
	req.Extra = extraFields(raw)
	return req, nil
}

func nullToNil(r json.RawMessage) json.RawMessage {
	if len(r) == 0 || bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
		return nil
	}
	return r
}

func extraFields(raw map[string]json.RawMessage) map[string]json.RawMessage {
	for _, k := range chatKnown {
		delete(raw, k)
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func decodeStop(r json.RawMessage) ([]string, *apiError) {
	r = nullToNil(r)
	if r == nil {
		return nil, nil
	}
	var one string
	if json.Unmarshal(r, &one) == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(r, &many); err != nil {
		return nil, invalid("stop", "stop must be a string or an array of strings")
	}
	return many, nil
}

func decodeMessage(m wireMessage) (chat.Message, error) {
	role := chat.Role(m.Role)
	switch role {
	case chat.RoleSystem, chat.RoleUser, chat.RoleAssistant, chat.RoleTool:
	default:
		return chat.Message{}, fmt.Errorf("unsupported role %q", m.Role)
	}
	msg := chat.Message{Role: role, Name: m.Name, ToolCallID: m.ToolCallID}
	if content := nullToNil(m.Content); content != nil {
		var err error
		if msg.Content, msg.ContentIsString, err = decodeContent(content); err != nil {
			return chat.Message{}, err
		}
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, chat.ToolCall{
			ID: tc.ID, Type: tc.Type, Function: chat.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return msg, nil
}

func decodeContent(content json.RawMessage) (parts []chat.Part, isString bool, err error) {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []chat.Part{{Type: chat.PartText, Text: s}}, true, nil
	}
	parts, err = decodeParts(content)
	return parts, false, err
}

func decodeParts(content json.RawMessage) ([]chat.Part, error) {
	var ps []wirePart
	if err := json.Unmarshal(content, &ps); err != nil {
		return nil, errors.New("content must be a string or an array of content parts")
	}
	parts := make([]chat.Part, 0, len(ps))
	for j, p := range ps {
		switch chat.PartType(p.Type) {
		case chat.PartText:
			parts = append(parts, chat.Part{Type: chat.PartText, Text: p.Text})
		case chat.PartImage:
			parts = append(parts, chat.Part{Type: chat.PartImage, ImageURL: p.ImageURL.URL, ImageDetail: p.ImageURL.Detail})
		default:
			return nil, fmt.Errorf("content[%d]: unsupported part type %q", j, p.Type)
		}
	}
	return parts, nil
}

type wireUsage struct {
	PromptTokens     int                `json:"prompt_tokens"`
	CompletionTokens int                `json:"completion_tokens"`
	TotalTokens      int                `json:"total_tokens"`
	PromptDetails    *wirePromptDetails `json:"prompt_tokens_details,omitempty"`
}

type wirePromptDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func encodeUsage(u chat.Usage) *wireUsage {
	w := &wireUsage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens()}
	if u.CachedPromptTokens > 0 {
		w.PromptDetails = &wirePromptDetails{CachedTokens: u.CachedPromptTokens}
	}
	return w
}

type wireResponse struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"`
	Created           int64        `json:"created"`
	Model             string       `json:"model"`
	Choices           []wireChoice `json:"choices"`
	Usage             *wireUsage   `json:"usage"`
	SystemFingerprint string       `json:"system_fingerprint,omitempty"`
}

type wireChoice struct {
	Index        int                `json:"index"`
	Message      wireOutMessage     `json:"message"`
	FinishReason *chat.FinishReason `json:"finish_reason"`
}

type wireOutMessage struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

func encodeResponse(resp chat.Response, id, modelName string, created int64) wireResponse {
	out := wireResponse{
		ID: orElse(resp.ID, id), Object: "chat.completion", Model: modelName,
		Created: created, Usage: encodeUsage(resp.Usage), SystemFingerprint: resp.SystemFingerprint,
		Choices: make([]wireChoice, 0, len(resp.Choices)),
	}
	if resp.Created != 0 {
		out.Created = resp.Created
	}
	for _, c := range resp.Choices {
		m := wireOutMessage{Role: string(orElse(c.Message.Role, chat.RoleAssistant))}
		if t := c.Message.Text(); t != "" || len(c.Message.ToolCalls) == 0 {
			m.Content = &t
		}
		for _, tc := range c.Message.ToolCalls {
			w := wireToolCall{ID: tc.ID, Type: orElse(tc.Type, "function")}
			w.Function.Name, w.Function.Arguments = tc.Function.Name, tc.Function.Arguments
			m.ToolCalls = append(m.ToolCalls, w)
		}
		wc := wireChoice{Index: c.Index, Message: m}
		if c.FinishReason != "" {
			fr := c.FinishReason
			wc.FinishReason = &fr
		}
		out.Choices = append(out.Choices, wc)
	}
	return out
}

func orElse[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

type wireChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []wireChunkChoice `json:"choices"`
	Usage   *wireUsage        `json:"usage,omitempty"`
}

type wireChunkChoice struct {
	Index        int                `json:"index"`
	Delta        wireDelta          `json:"delta"`
	FinishReason *chat.FinishReason `json:"finish_reason"`
}

type wireDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []wireToolDelta `json:"tool_calls,omitempty"`
}

type wireToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func encodeChunk(c chat.Chunk, id string, created int64, withUsage bool) wireChunk {
	out := wireChunk{
		ID: orElse(c.ID, id), Object: "chat.completion.chunk", Model: c.Model,
		Created: orElse(c.Created, created), Choices: make([]wireChunkChoice, 0, len(c.Choices)),
	}
	if withUsage && c.Usage != nil {
		out.Usage = encodeUsage(*c.Usage)
	}
	for _, ch := range c.Choices {
		d := wireDelta{Role: string(ch.Delta.Role), Content: ch.Delta.Content}
		for _, tc := range ch.Delta.ToolCalls {
			t := wireToolDelta{Index: tc.Index, ID: tc.ID, Type: tc.Type}
			t.Function.Name, t.Function.Arguments = tc.Name, tc.Arguments
			d.ToolCalls = append(d.ToolCalls, t)
		}
		wc := wireChunkChoice{Index: ch.Index, Delta: d}
		if ch.FinishReason != "" {
			fr := ch.FinishReason
			wc.FinishReason = &fr
		}
		out.Choices = append(out.Choices, wc)
	}
	return out
}
