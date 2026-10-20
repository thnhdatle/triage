// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

type wireRequest struct {
	Model         string          `json:"model"`
	System        string          `json:"system,omitempty"`
	Messages      []wireMessage   `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Tools         []wireTool      `json:"tools,omitempty"`
	ToolChoice    *wireToolChoice `json:"tool_choice,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	Metadata      *wireMetadata   `json:"metadata,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
}

type wireMetadata struct {
	UserID string `json:"user_id"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

// wireBlock is a request content block: text, image, tool_use or tool_result.
type wireBlock struct {
	Type      string           `json:"type"`
	Text      string           `json:"text,omitempty"`
	Source    *wireImageSource `json:"source,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     json.RawMessage  `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	Content   string           `json:"content,omitempty"`
}

type wireImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	Strict      *bool           `json:"strict,omitempty"`
}

type wireToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// Content block and role names of the Messages API.
const (
	blockText       = "text"
	blockImage      = "image"
	blockToolUse    = "tool_use"
	blockToolResult = "tool_result"
	roleUser        = "user"
	roleAssistant   = "assistant"
	toolTypeFunc    = "function"
	choiceAuto      = "auto"
	jsonNull        = "null"
)

// emptySchema is sent for a tool without parameters; input_schema is required.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// imageMediaTypes are the base64 image types the Messages API accepts.
var imageMediaTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

// encodeRequest translates req. OpenAI-only fields (seed, unknown extra
// fields) are dropped; response_format other than text is refused.
func (c *Client) encodeRequest(req chat.Request, stream bool) ([]byte, error) {
	w := wireRequest{
		Model: req.Model, MaxTokens: req.RequestedOutputTokens(), StopSequences: req.Stop,
		Temperature: req.Temperature, TopP: req.TopP, Stream: stream,
	}
	if w.MaxTokens <= 0 {
		w.MaxTokens = c.defaultMaxTokens
	}
	if req.User != "" {
		w.Metadata = &wireMetadata{UserID: req.User}
	}
	if err := checkResponseFormat(req.ResponseFormat); err != nil {
		return nil, err
	}
	system, msgs, err := encodeMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	w.System, w.Messages = system, msgs
	if w.Tools, err = encodeTools(req.Tools); err != nil {
		return nil, err
	}
	if w.ToolChoice, err = encodeToolChoice(req.ToolChoice, req.Extra["parallel_tool_calls"]); err != nil {
		return nil, err
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	return data, nil
}

func checkResponseFormat(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == jsonNull {
		return nil
	}
	var rf struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &rf) != nil || rf.Type != "text" {
		return unsupported("response_format is not supported by the Messages API")
	}
	return nil
}

// encodeMessages extracts system messages into the system prompt, maps the
// rest to Messages API turns and merges consecutive turns of the same role
// (tool results become user turns).
func encodeMessages(msgs []chat.Message) (string, []wireMessage, error) {
	var (
		system []string
		out    []wireMessage
	)
	for i, m := range msgs {
		if m.Role == chat.RoleSystem {
			if m.HasImage() {
				return "", nil, unsupported("message " + strconv.Itoa(i) + ": images in system messages")
			}
			system = append(system, m.Text())
			continue
		}
		role, blocks, err := encodeMessage(m)
		if err != nil {
			return "", nil, fmt.Errorf("message %d: %w", i, err)
		}
		if len(blocks) == 0 {
			return "", nil, unsupported("message " + strconv.Itoa(i) + " has no content")
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			continue
		}
		out = append(out, wireMessage{Role: role, Content: blocks})
	}
	return strings.Join(system, "\n\n"), out, nil
}

func encodeMessage(m chat.Message) (string, []wireBlock, error) {
	switch m.Role {
	case chat.RoleTool:
		return roleUser, []wireBlock{{Type: blockToolResult, ToolUseID: m.ToolCallID, Content: m.Text()}}, nil
	case chat.RoleUser, chat.RoleAssistant:
	case chat.RoleSystem:
		return "", nil, unsupported("system message inside the conversation") // extracted by encodeMessages
	default:
		return "", nil, unsupported("unsupported role " + strconv.Quote(string(m.Role)))
	}
	blocks := make([]wireBlock, 0, len(m.Content)+len(m.ToolCalls))
	for _, p := range m.Content {
		b, ok, err := encodePart(p)
		if err != nil {
			return "", nil, err
		}
		if ok {
			blocks = append(blocks, b)
		}
	}
	for _, tc := range m.ToolCalls {
		input, err := toolInput(tc.Function.Arguments)
		if err != nil {
			return "", nil, err
		}
		blocks = append(blocks, wireBlock{Type: blockToolUse, ID: tc.ID, Name: tc.Function.Name, Input: input})
	}
	return string(m.Role), blocks, nil
}

// encodePart maps a content part; empty text parts are dropped because the
// Messages API rejects empty text blocks.
func encodePart(p chat.Part) (wireBlock, bool, error) {
	switch p.Type {
	case chat.PartText:
		return wireBlock{Type: blockText, Text: p.Text}, p.Text != "", nil
	case chat.PartImage:
		src, err := imageSource(p.ImageURL)
		return wireBlock{Type: blockImage, Source: src}, err == nil, err
	}
	return wireBlock{}, false, unsupported("unsupported content part type " + strconv.Quote(string(p.Type)))
}

// imageSource maps a data: URL to a base64 source and an http(s) URL to a
// url source.
func imageSource(u string) (*wireImageSource, error) {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return &wireImageSource{Type: "url", URL: u}, nil
	}
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return nil, unsupported("image url must be an http(s) or data URL")
	}
	meta, data, ok := strings.Cut(rest, ",")
	mediaType, isBase64 := strings.CutSuffix(meta, ";base64")
	if !ok || !isBase64 || data == "" {
		return nil, unsupported("image data URL must be base64 encoded")
	}
	if !imageMediaTypes[mediaType] {
		return nil, unsupported("unsupported image media type " + strconv.Quote(mediaType))
	}
	return &wireImageSource{Type: "base64", MediaType: mediaType, Data: data}, nil
}

// toolInput turns OpenAI tool-call arguments (a JSON string) into the
// tool_use input object.
func toolInput(args string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace([]byte(args))
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, unsupported("tool call arguments must be a JSON object")
	}
	return trimmed, nil
}

func encodeTools(tools []chat.Tool) ([]wireTool, error) {
	out := make([]wireTool, 0, len(tools))
	for _, t := range tools {
		if t.Type != "" && t.Type != toolTypeFunc {
			return nil, unsupported("unsupported tool type " + strconv.Quote(t.Type))
		}
		schema := t.Function.Parameters
		if len(bytes.TrimSpace(schema)) == 0 || string(schema) == jsonNull {
			schema = emptySchema
		}
		out = append(out, wireTool{
			Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema, Strict: t.Function.Strict,
		})
	}
	return out, nil
}

// encodeToolChoice maps OpenAI tool_choice ("auto", "none", "required" or a
// named function) and parallel_tool_calls: false.
func encodeToolChoice(raw, parallel json.RawMessage) (*wireToolChoice, error) {
	noParallel := string(bytes.TrimSpace(parallel)) == "false"
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == jsonNull {
		if noParallel {
			return &wireToolChoice{Type: choiceAuto, DisableParallelToolUse: true}, nil
		}
		return nil, nil //nolint:nilnil // no tool_choice is a valid translation
	}
	tc := &wireToolChoice{DisableParallelToolUse: noParallel}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case choiceAuto:
			tc.Type = choiceAuto
		case "none":
			tc.Type, tc.DisableParallelToolUse = "none", false
		case "required":
			tc.Type = "any"
		default:
			return nil, unsupported("unsupported tool_choice " + strconv.Quote(s))
		}
		return tc, nil
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &obj) != nil || obj.Type != toolTypeFunc || obj.Function.Name == "" {
		return nil, unsupported("unsupported tool_choice")
	}
	tc.Type, tc.Name = "tool", obj.Function.Name
	return tc, nil
}
