// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

var (
	_ provider.Provider = (*Client)(nil)
	_ provider.Stream   = (*stream)(nil)
)

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (u wireUsage) usage() chat.Usage {
	return chat.Usage{
		PromptTokens:       u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
		CompletionTokens:   u.OutputTokens,
		CachedPromptTokens: u.CacheReadInputTokens,
	}
}

type wireContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type wireResponse struct {
	ID         string        `json:"id"`
	Type       string        `json:"type"`
	Model      string        `json:"model"`
	Content    []wireContent `json:"content"`
	StopReason string        `json:"stop_reason"`
	Usage      wireUsage     `json:"usage"`
}

func finishReason(s string) chat.FinishReason {
	switch s {
	case "":
		return ""
	case "max_tokens", "model_context_window_exceeded":
		return chat.FinishLength
	case "tool_use":
		return chat.FinishToolCalls
	case "refusal":
		return chat.FinishContentFilter
	}
	return chat.FinishStop
}

func arguments(input json.RawMessage) string {
	var b bytes.Buffer
	if len(input) == 0 || json.Compact(&b, input) != nil || b.String() == jsonNull {
		return "{}"
	}
	return b.String()
}

func (c *Client) decodeResponse(data []byte, status int) (chat.Response, error) {
	var w wireResponse
	if err := json.Unmarshal(data, &w); err != nil || w.Type != "message" {
		return chat.Response{}, badUpstream(status, "not a message")
	}
	msg := chat.Message{Role: chat.RoleAssistant, ContentIsString: true}
	var text strings.Builder
	for _, b := range w.Content {
		switch b.Type {
		case blockText:
			text.WriteString(b.Text)
		case blockToolUse:
			msg.ToolCalls = append(msg.ToolCalls, chat.ToolCall{
				ID: b.ID, Type: toolTypeFunc, Function: chat.FunctionCall{Name: b.Name, Arguments: arguments(b.Input)},
			})
		}
	}
	if text.Len() > 0 || len(msg.ToolCalls) == 0 {
		msg.Content = []chat.Part{{Type: chat.PartText, Text: text.String()}}
	}
	return chat.Response{
		ID: w.ID, Model: w.Model, Created: c.clk.Now().Unix(), Usage: w.Usage.usage(),
		Choices: []chat.Choice{{Message: msg, FinishReason: finishReason(w.StopReason)}},
	}, nil
}

// Complete sends a message request.
func (c *Client) Complete(ctx context.Context, req chat.Request) (chat.Response, error) {
	body, err := c.encodeRequest(req, false)
	if err != nil {
		return chat.Response{}, err
	}
	data, status, err := c.fetch(ctx, http.MethodPost, "/messages", req.Model, body)
	if err != nil {
		return chat.Response{}, err
	}
	return c.decodeResponse(data, status)
}

// Stream sends a streaming message request.
func (c *Client) Stream(ctx context.Context, req chat.Request) (provider.Stream, error) {
	body, err := c.encodeRequest(req, true)
	if err != nil {
		return nil, err
	}
	//nolint:bodyclose // the body is owned and closed by the returned stream
	resp, err := c.do(ctx, http.MethodPost, "/messages", req.Model, body)
	if err != nil {
		return nil, err
	}
	return c.newStream(ctx, resp, req.IncludeUsage), nil
}

func (c *Client) Embed(context.Context, provider.EmbedRequest) (provider.EmbedResponse, error) {
	return provider.EmbedResponse{}, unsupported("embeddings are not supported")
}

const maxModelPages = 20

func (c *Client) Models(ctx context.Context) ([]string, error) {
	var names []string
	after := ""
	for range maxModelPages {
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after_id", after)
		}
		data, status, err := c.fetch(ctx, http.MethodGet, "/models?"+q.Encode(), "", nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(data, &page); err != nil || page.Data == nil {
			return nil, badUpstream(status, "invalid model list")
		}
		for _, m := range page.Data {
			names = append(names, m.ID)
		}
		if !page.HasMore || page.LastID == "" {
			return names, nil
		}
		after = page.LastID
	}
	return names, nil
}
