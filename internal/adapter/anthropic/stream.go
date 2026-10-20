// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// errClosed is returned by Recv after Close.
var errClosed = errors.New("anthropic: stream closed")

// event is one stream event; only the fields the adapter uses are decoded.
type event struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage wireUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *wireContent `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *wireUsage      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// stream translates Messages API events into neutral chunks. Tool-use
// blocks are numbered in order of appearance, as OpenAI tool-call indexes.
type stream struct {
	body         io.ReadCloser
	lines        *bufio.Scanner
	status       int
	ctxErr       func() error
	includeUsage bool
	created      int64

	id, model string
	usage     wireUsage
	toolIndex map[int]int

	err  error
	once sync.Once
}

func (c *Client) newStream(ctx context.Context, resp *http.Response, includeUsage bool) *stream {
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), c.maxBytes)
	return &stream{
		body: resp.Body, lines: sc, status: resp.StatusCode, includeUsage: includeUsage,
		created: c.clk.Now().Unix(), toolIndex: map[int]int{},
		ctxErr: func() error {
			if ctx.Err() == nil {
				return nil
			}
			return requestError(ctx, ctx.Err())
		},
	}
}

// Recv implements provider.Stream.
func (s *stream) Recv() (chat.Chunk, error) {
	for s.err == nil {
		data, err := s.nextData()
		if err != nil {
			s.err = err
			break
		}
		var ev event
		if json.Unmarshal(data, &ev) != nil {
			s.err = badUpstream(s.status, "invalid stream event")
			break
		}
		c, ok, err := s.translate(&ev)
		if err != nil {
			s.err = err
			break
		}
		if ok {
			return c, nil
		}
	}
	return chat.Chunk{}, s.err
}

// nextData returns the data of the next "data:" line. Event names are not
// needed: every data object carries its type.
func (s *stream) nextData() ([]byte, error) {
	for s.lines.Scan() {
		if v, ok := strings.CutPrefix(s.lines.Text(), "data:"); ok {
			return []byte(strings.TrimPrefix(v, " ")), nil
		}
	}
	if err := s.ctxErr(); err != nil {
		return nil, err
	}
	err := s.lines.Err()
	switch {
	case errors.Is(err, bufio.ErrTooLong):
		return nil, badUpstream(s.status, "stream line too large")
	case err != nil:
		return nil, connError(err)
	}
	return nil, &provider.Error{
		Kind: provider.ErrUpstreamUnavailable, StatusCode: s.status, Message: "stream ended without message_stop",
	}
}

func (s *stream) chunk(choices ...chat.ChunkChoice) chat.Chunk {
	if choices == nil {
		choices = []chat.ChunkChoice{}
	}
	return chat.Chunk{ID: s.id, Model: s.model, Created: s.created, Choices: choices}
}

func (s *stream) delta(d chat.Delta) chat.Chunk { return s.chunk(chat.ChunkChoice{Delta: d}) }

// translate maps one event; ok is false for events without client-visible
// content (ping, block stops, thinking deltas, unknown types).
func (s *stream) translate(ev *event) (chat.Chunk, bool, error) {
	switch ev.Type {
	case "message_start":
		if ev.Message == nil {
			return chat.Chunk{}, false, badUpstream(s.status, "message_start without message")
		}
		s.id, s.model, s.usage = ev.Message.ID, ev.Message.Model, ev.Message.Usage
		return s.delta(chat.Delta{Role: chat.RoleAssistant}), true, nil
	case "content_block_start":
		return s.blockStart(ev)
	case "content_block_delta":
		return s.blockDelta(ev)
	case "message_delta":
		s.mergeUsage(ev.Usage)
		if ev.Delta == nil || ev.Delta.StopReason == "" {
			return chat.Chunk{}, false, nil
		}
		return s.chunk(chat.ChunkChoice{FinishReason: finishReason(ev.Delta.StopReason)}), true, nil
	case "message_stop":
		return s.stop()
	case "error":
		a := parseAPIError(mustObject(ev.Error))
		return chat.Chunk{}, false, &provider.Error{Kind: a.kind(), StatusCode: s.status, Message: a.describe(s.status)}
	}
	return chat.Chunk{}, false, nil
}

// mustObject wraps a raw error object back into an error envelope.
func mustObject(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		raw = json.RawMessage(jsonNull)
	}
	return append(append([]byte(`{"error":`), raw...), '}')
}

func (s *stream) blockStart(ev *event) (chat.Chunk, bool, error) {
	b := ev.ContentBlock
	if b == nil {
		return chat.Chunk{}, false, badUpstream(s.status, "content_block_start without block")
	}
	switch b.Type {
	case blockToolUse:
		idx := len(s.toolIndex)
		s.toolIndex[ev.Index] = idx
		return s.delta(chat.Delta{ToolCalls: []chat.ToolCallDelta{{
			Index: idx, ID: b.ID, Type: toolTypeFunc, Name: b.Name,
		}}}), true, nil
	case blockText:
		return s.delta(chat.Delta{Content: b.Text}), b.Text != "", nil
	}
	return chat.Chunk{}, false, nil
}

func (s *stream) blockDelta(ev *event) (chat.Chunk, bool, error) {
	d := ev.Delta
	if d == nil {
		return chat.Chunk{}, false, badUpstream(s.status, "content_block_delta without delta")
	}
	switch d.Type {
	case "text_delta":
		return s.delta(chat.Delta{Content: d.Text}), d.Text != "", nil
	case "input_json_delta":
		idx, ok := s.toolIndex[ev.Index]
		if !ok {
			return chat.Chunk{}, false, badUpstream(s.status, "input_json_delta for unknown block")
		}
		return s.delta(chat.Delta{ToolCalls: []chat.ToolCallDelta{{Index: idx, Arguments: d.PartialJSON}}}),
			d.PartialJSON != "", nil
	}
	return chat.Chunk{}, false, nil // thinking, signature and citation deltas are not relayed
}

// mergeUsage applies message_delta usage: output tokens are cumulative, and
// input counts, when present, replace those of message_start.
func (s *stream) mergeUsage(u *wireUsage) {
	if u == nil {
		return
	}
	s.usage.OutputTokens = u.OutputTokens
	if u.InputTokens > 0 {
		s.usage.InputTokens = u.InputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		s.usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		s.usage.CacheReadInputTokens = u.CacheReadInputTokens
	}
}

// stop ends the stream: the usage chunk when requested, then io.EOF.
func (s *stream) stop() (chat.Chunk, bool, error) {
	if !s.includeUsage {
		return chat.Chunk{}, false, io.EOF
	}
	c := s.chunk()
	u := s.usage.usage()
	c.Usage = &u
	s.err = io.EOF
	return c, true, nil
}

// Close implements provider.Stream; it is idempotent.
func (s *stream) Close() error {
	var err error
	s.once.Do(func() {
		if s.err == nil {
			s.err = errClosed
		}
		err = s.body.Close()
	})
	if err != nil {
		return &provider.Error{Kind: provider.ErrUpstreamUnavailable, Message: "close stream: " + err.Error()}
	}
	return nil
}
