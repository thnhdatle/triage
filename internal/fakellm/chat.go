// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultChunkBytes = 16
	timeoutMax        = 10 * time.Minute
	okWordCount       = 6
)

// okWords is the constant table deterministic completions draw from.
var okWords = [...]string{
	"alpha", "bravo", "cedar", "delta", "ember", "flint", "grove", "harbor",
	"iris", "juniper", "kestrel", "lumen", "meadow", "nectar", "onyx", "prairie",
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatRequest struct {
	Model         string        `json:"model"`
	Messages      []chatMessage `json:"messages"`
	Stream        bool          `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

// messageText returns the text of a content value: a string, or the
// concatenated text parts of an array.
func messageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text != nil {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}

// params are the effective scenario parameters of one request.
type params struct {
	scenario   string
	cutAfter   int
	latency    time.Duration
	answer     *string
	chunkBytes int
	splitRunes bool
	chunkDelay time.Duration
}

// resolveParams merges the script rule (if any) and the X-Fake-* headers;
// headers win. A non-nil error message is a client error.
func resolveParams(h http.Header, rule *scriptRule) (params, error) {
	p := params{scenario: ScenarioOK, cutAfter: 1, chunkBytes: defaultChunkBytes}
	if rule != nil {
		p.scenario = rule.Scenario
		if v := rule.Params.CutAfter; v != nil {
			p.cutAfter = *v
		}
		if v := rule.Params.LatencyMs; v != nil {
			p.latency = time.Duration(*v) * time.Millisecond
		}
		p.answer = rule.Params.Answer
		if v := rule.Params.ChunkBytes; v != nil {
			p.chunkBytes = *v
		}
	}
	if v := h.Get("X-Fake-Scenario"); v != "" {
		p.scenario = v
	}
	if v := h.Get("X-Fake-Answer"); v != "" || h.Values("X-Fake-Answer") != nil {
		p.answer = &v
	}
	p.splitRunes = strings.EqualFold(h.Get("X-Fake-Chunk-Split-Runes"), "true")
	var err error
	if p.cutAfter, err = intHeader(h, "X-Fake-Cut-After", p.cutAfter); err != nil {
		return p, err
	}
	if p.chunkBytes, err = intHeader(h, "X-Fake-Chunk-Bytes", p.chunkBytes); err != nil {
		return p, err
	}
	ms := int(p.latency / time.Millisecond)
	if ms, err = intHeader(h, "X-Fake-Latency-Ms", ms); err != nil {
		return p, err
	}
	p.latency = time.Duration(ms) * time.Millisecond
	cd, err := intHeader(h, "X-Fake-Chunk-Delay-Ms", 0)
	p.chunkDelay = time.Duration(cd) * time.Millisecond
	return p, err
}

func intHeader(h http.Header, name string, def int) (int, error) {
	v := h.Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("header %s must be a non-negative integer", name)
	}
	return n, nil
}

// okText derives a few words from SHA-256 over model and messages.
func okText(model string, req *chatRequest) string {
	h := sha256.New()
	h.Write([]byte(model))
	for _, m := range req.Messages {
		h.Write([]byte("\x00" + m.Role + "\x00" + messageText(m.Content)))
	}
	sum := h.Sum(nil)
	words := make([]string, okWordCount)
	for i := range words {
		words[i] = okWords[int(sum[i])%len(okWords)]
	}
	return strings.Join(words, " ")
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxBodyBytes)
	if !ok {
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.record(r, body, "", KindChat, 0)
		badRequest(w, "invalid JSON body: "+err.Error())
		return
	}
	if req.Model == "" {
		req.Model = DefaultModel
	}
	s.record(r, body, req.Model, KindChat, 0)

	hasHeader := r.Header.Get("X-Fake-Scenario") != ""
	n, rule, ok := s.begin(req.Model, hasHeader)
	if !ok {
		badRequest(w, "too many distinct models")
		return
	}
	p, err := resolveParams(r.Header, rule)
	if err != nil {
		badRequest(w, err.Error())
		return
	}

	lastUser := ""
	promptTokens := 0
	for _, m := range req.Messages {
		t := messageText(m.Content)
		promptTokens += tokenCount(t)
		if m.Role == "user" {
			lastUser = t
		}
	}
	c := &chatCall{
		s: s, w: w, r: r, req: &req, p: p, n: n, lastUser: lastUser,
		id: chatID(req.Model, n), created: s.clock.Now().Unix(), promptTokens: promptTokens,
	}
	c.serve(r.Context())
}

func chatID(model string, n int) string {
	sum := sha256.Sum256([]byte(model + "\x00" + strconv.Itoa(n)))
	return "chatcmpl-" + hex.EncodeToString(sum[:8])
}

// chatCall holds the state of one chat completion request.
type chatCall struct {
	s            *Server
	w            http.ResponseWriter
	r            *http.Request
	req          *chatRequest
	p            params
	n            int
	lastUser     string
	id           string
	created      int64
	promptTokens int
}

func (c *chatCall) serve(ctx context.Context) {
	switch c.p.scenario {
	case ScenarioTimeout:
		_ = c.s.wait(ctx, timeoutMax)
		panic(http.ErrAbortHandler)
	case Scenario429:
		c.w.Header().Set("Retry-After", "1")
		writeError(c.w, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded",
			"Rate limit reached (fakellm scenario 429)")
		return
	case Scenario500:
		writeError(c.w, http.StatusInternalServerError, "server_error", "internal_error",
			"The server had an error (fakellm scenario 500)")
		return
	case ScenarioSlow:
		if c.s.wait(ctx, c.p.latency) != nil {
			return
		}
	case ScenarioOK, ScenarioEcho, ScenarioToolCall, ScenarioCutStream, ScenarioInvalidJSON:
	default:
		badRequest(c.w, "unknown X-Fake-Scenario "+strconv.Quote(c.p.scenario))
		return
	}
	c.respond(ctx)
}

// output is what the scenario produces: text, or a tool call.
type output struct {
	text     string
	toolName string
	toolArgs string
	isTool   bool
}

func (o output) completionTokens() int {
	if o.isTool {
		return tokenCount(o.toolArgs)
	}
	return tokenCount(o.text)
}

func (c *chatCall) output() output {
	if c.p.scenario == ScenarioToolCall && len(c.req.Tools) > 0 {
		return output{
			isTool:   true,
			toolName: c.req.Tools[0].Function.Name,
			toolArgs: `{"input": ` + string(marshal(c.lastUser)) + `}`,
		}
	}
	text := ""
	switch {
	case c.p.answer != nil:
		text = strings.NewReplacer("{n}", strconv.Itoa(c.n), "{user}", c.lastUser).Replace(*c.p.answer)
	case c.p.scenario == ScenarioEcho:
		text = c.lastUser
	default:
		text = okText(c.req.Model, c.req)
	}
	return output{text: text}
}

func (c *chatCall) usage(o output) usage {
	ct := o.completionTokens()
	return usage{PromptTokens: c.promptTokens, CompletionTokens: ct, TotalTokens: c.promptTokens + ct}
}

func (c *chatCall) respond(ctx context.Context) {
	o := c.output()
	switch {
	case c.req.Stream:
		c.stream(ctx, o)
	case c.p.scenario == ScenarioCutStream:
		panic(http.ErrAbortHandler)
	default:
		c.plain(o)
	}
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (c *chatCall) plain(o output) {
	msg := map[string]any{"role": "assistant", "content": o.text}
	finish := "stop"
	if o.isTool {
		tc := toolCall{ID: "call_" + c.id[len("chatcmpl-"):], Type: "function"}
		tc.Function.Name, tc.Function.Arguments = o.toolName, o.toolArgs
		msg["content"] = nil
		msg["tool_calls"] = []toolCall{tc}
		finish = "tool_calls"
	}
	data := marshal(map[string]any{
		"id": c.id, keyObject: "chat.completion", "created": c.created, keyModel: c.req.Model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   c.usage(o),
	})
	c.w.Header().Set("Content-Type", "application/json")
	c.w.WriteHeader(http.StatusOK)
	if c.p.scenario == ScenarioInvalidJSON {
		data = data[:len(data)/2]
	}
	_, _ = c.w.Write(data)
}

// deltas builds the raw JSON delta objects of a streamed response.
func (c *chatCall) deltas(o output) []string {
	var out []string
	if o.isTool {
		first := fmt.Sprintf(
			`{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":%s,"type":"function",`+
				`"function":{"name":%s,"arguments":""}}]}`,
			quoteBytes("call_"+c.id[len("chatcmpl-"):]), quoteBytes(o.toolName))
		out = append(out, first)
		for _, piece := range splitChunks(o.toolArgs, c.p.chunkBytes, c.p.splitRunes) {
			out = append(out, `{"tool_calls":[{"index":0,"function":{"arguments":`+quoteBytes(piece)+`}}]}`)
		}
		return out
	}
	for i, piece := range splitChunks(o.text, c.p.chunkBytes, c.p.splitRunes) {
		role := ""
		if i == 0 {
			role = `"role":"assistant",`
		}
		out = append(out, `{`+role+`"content":`+quoteBytes(piece)+`}`)
	}
	return out
}

func (c *chatCall) chunk(choices, extra string) string {
	return fmt.Sprintf(`{"id":%s,"object":"chat.completion.chunk","created":%d,"model":%s,"choices":[%s]%s}`,
		quoteBytes(c.id), c.created, quoteBytes(c.req.Model), choices, extra)
}

func (c *chatCall) stream(ctx context.Context, o output) {
	h := c.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(c.w)
	if c.p.scenario == ScenarioInvalidJSON {
		c.w.WriteHeader(http.StatusOK)
		malformed := `data: {"id":"` + c.id + `","object":"chat.completion.chunk","choices":[{"index":0,"delta":`
		_, _ = c.w.Write([]byte(malformed + "\n\n"))
		return
	}
	finish := "stop"
	if o.isTool {
		finish = "tool_calls"
	}
	var chunks []string
	for _, d := range c.deltas(o) {
		chunks = append(chunks, c.chunk(`{"index":0,"delta":`+d+`,"finish_reason":null}`, ""))
	}
	chunks = append(chunks, c.chunk(`{"index":0,"delta":{},"finish_reason":"`+finish+`"}`, ""))
	if c.req.StreamOptions != nil && c.req.StreamOptions.IncludeUsage {
		chunks = append(chunks, c.chunk("", `,"usage":`+string(marshal(c.usage(o)))))
	}

	cut := c.p.scenario == ScenarioCutStream
	if cut && c.p.cutAfter == 0 {
		panic(http.ErrAbortHandler)
	}
	c.w.WriteHeader(http.StatusOK)
	for i, ch := range chunks {
		if i > 0 && c.s.wait(ctx, c.p.chunkDelay) != nil {
			return
		}
		_, _ = c.w.Write([]byte("data: " + ch + "\n\n"))
		_ = rc.Flush()
		if cut && i+1 >= c.p.cutAfter {
			panic(http.ErrAbortHandler)
		}
	}
	if cut {
		panic(http.ErrAbortHandler)
	}
	_, _ = c.w.Write([]byte("data: [DONE]\n\n"))
	_ = rc.Flush()
}
