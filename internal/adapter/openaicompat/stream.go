// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// errEventTooLarge ends a stream whose event exceeds the size limit.
var errEventTooLarge = errors.New("openaicompat: stream event too large")

// sseReader splits a text/event-stream body into the data of each event.
type sseReader struct {
	r   *bufio.Reader
	max int64
}

// next returns the data of the next event (multi-line data joined by "\n").
// At the end of the body it returns io.EOF when no event was pending and
// io.ErrUnexpectedEOF when one was cut.
func (s *sseReader) next() (string, error) {
	var ev sseEvent
	for {
		line, err := s.line()
		if err != nil {
			return ev.atEOF(err)
		}
		if len(line) == 0 {
			if ev.hasData {
				return ev.data.String(), nil
			}
			ev.pending = false
			continue
		}
		ev.pending = true
		if err := ev.add(line, s.max); err != nil {
			return "", err
		}
	}
}

// doneMarker is the data of the terminal OpenAI stream event.
const doneMarker = "[DONE]"

// sseEvent accumulates the lines of one event.
type sseEvent struct {
	data    bytes.Buffer
	hasData bool
	pending bool
}

// atEOF resolves a read error: a pending [DONE] without its blank line is
// still the end marker, any other pending event was cut.
func (e *sseEvent) atEOF(err error) (string, error) {
	if !errors.Is(err, io.EOF) || !e.pending {
		return "", err
	}
	if e.hasData && e.data.String() == doneMarker {
		return doneMarker, nil
	}
	return "", io.ErrUnexpectedEOF
}

// add appends a data line; comments, event, id and retry fields carry
// nothing the adapter uses.
func (e *sseEvent) add(line []byte, limit int64) error {
	field, value, _ := bytes.Cut(line, []byte(":"))
	if string(field) != "data" {
		return nil
	}
	value = bytes.TrimPrefix(value, []byte(" "))
	if e.hasData {
		e.data.WriteByte('\n')
	}
	e.hasData = true
	if int64(e.data.Len()+len(value)) > limit {
		return errEventTooLarge
	}
	e.data.Write(value)
	return nil
}

// line reads one line without its terminator, bounded by max.
func (s *sseReader) line() ([]byte, error) {
	var buf []byte
	for {
		frag, err := s.r.ReadSlice('\n')
		buf = append(buf, frag...)
		if int64(len(buf)) > s.max {
			return nil, errEventTooLarge
		}
		switch {
		case err == nil:
			return bytes.TrimRight(buf, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			// A last line without terminator; the next read reports io.EOF.
			return bytes.TrimRight(buf, "\r\n"), nil
		default:
			return nil, err //nolint:wrapcheck // mapped by stream.fail
		}
	}
}

// stream implements provider.Stream over an SSE response.
type stream struct {
	body    io.ReadCloser
	events  sseReader
	status  int
	ctxFail func() error
	err     error
	once    sync.Once
}

func newStream(ctx context.Context, resp *http.Response, maxBytes int64) *stream {
	return &stream{
		body:   resp.Body,
		events: sseReader{r: bufio.NewReader(resp.Body), max: maxBytes},
		status: resp.StatusCode,
		ctxFail: func() error {
			if ctx.Err() == nil {
				return nil
			}
			return transportError(ctx, ctx.Err())
		},
	}
}

// Recv implements provider.Stream.
func (s *stream) Recv() (chat.Chunk, error) {
	if s.err != nil {
		return chat.Chunk{}, s.err
	}
	for {
		data, err := s.events.next()
		if err != nil {
			s.err = s.fail(err)
			return chat.Chunk{}, s.err
		}
		if data == doneMarker {
			s.err = io.EOF
			return chat.Chunk{}, io.EOF
		}
		c, ok, err := decodeChunk([]byte(data), s.status)
		if err != nil {
			s.err = err
			return chat.Chunk{}, err
		}
		if ok {
			return c, nil
		}
	}
}

func (s *stream) fail(err error) error {
	if cerr := s.ctxFail(); cerr != nil {
		return cerr
	}
	switch {
	case errors.Is(err, errEventTooLarge):
		return malformed(s.status, "stream event too large")
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &provider.Error{
			Kind: provider.ErrUpstreamUnavailable, StatusCode: s.status, Message: "stream ended without [DONE]",
		}
	}
	return netError(err)
}

// Close implements provider.Stream; it is idempotent.
func (s *stream) Close() error {
	var err error
	s.once.Do(func() {
		if s.err == nil {
			s.err = errStreamClosed
		}
		err = s.body.Close()
	})
	if err != nil {
		return &provider.Error{Kind: provider.ErrUpstreamUnavailable, Message: "close stream: " + err.Error()}
	}
	return nil
}

// errStreamClosed is returned by Recv after Close.
var errStreamClosed = errors.New("openaicompat: stream closed")

type wireChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// decodeChunk translates one stream event; ok is false for events that
// carry neither choices nor usage.
func decodeChunk(data []byte, status int) (chat.Chunk, bool, error) {
	var w wireChunk
	if err := json.Unmarshal(data, &w); err != nil {
		return chat.Chunk{}, false, malformed(status, "invalid stream event")
	}
	if len(w.Error) > 0 && string(w.Error) != jsonNull {
		return chat.Chunk{}, false, streamError(data, status)
	}
	if len(w.Choices) == 0 && w.Usage == nil {
		return chat.Chunk{}, false, nil
	}
	c := chat.Chunk{ID: w.ID, Model: w.Model, Created: w.Created, Choices: make([]chat.ChunkChoice, 0, len(w.Choices))}
	if w.Usage != nil {
		u := w.Usage.usage()
		c.Usage = &u
	}
	for _, ch := range w.Choices {
		cc := chat.ChunkChoice{
			Index: ch.Index, Delta: chat.Delta{Role: chat.Role(ch.Delta.Role), Content: ch.Delta.Content},
		}
		if ch.FinishReason != nil {
			cc.FinishReason = chat.FinishReason(*ch.FinishReason)
		}
		for _, tc := range ch.Delta.ToolCalls {
			cc.Delta.ToolCalls = append(cc.Delta.ToolCalls, chat.ToolCallDelta{
				Index: tc.Index, ID: tc.ID, Type: tc.Type, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
			})
		}
		c.Choices = append(c.Choices, cc)
	}
	return c, true, nil
}

// streamError maps an error event sent inside a 200 stream.
func streamError(data []byte, status int) error {
	info := parseErrorBody(data)
	e := &provider.Error{Kind: provider.ErrUpstreamUnavailable, StatusCode: status, Message: info.describe(status)}
	switch {
	case info.typ == "rate_limit_error" || info.code == "rate_limit_exceeded":
		e.Kind = provider.ErrRateLimited
	case info.isContextLength():
		e.Kind = provider.ErrContextLength
	case info.typ == "invalid_request_error":
		e.Kind = provider.ErrBadRequest
	}
	return e
}
