// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"sort"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// ContentStream is the StreamID.ToolCall of a choice's content stream.
const ContentStream = -1

// StreamID identifies one output stream of a streamed completion: the content
// of a choice (ToolCall == ContentStream) or the arguments of the tool call
// with that index in a choice.
type StreamID struct {
	Choice   int
	ToolCall int
}

// Pending is text a StreamUnmasker still held for one stream when it was
// flushed.
type Pending struct {
	StreamID
	Text string
}

// StreamUnmasker unmasks the deltas of one streamed masked attempt. Every
// stream holds back at most the longest placeholder length minus one bytes,
// and only while they are a proper prefix of a placeholder, so a placeholder
// split across deltas at any offset is restored exactly and no byte that may
// begin a placeholder is emitted early. Partial detection runs on the
// reassembled upstream text of each stream, so tokens split across deltas are
// detected too.
//
// A StreamUnmasker is not safe for concurrent use; create one per attempt,
// after any Rescan of its mapping.
type StreamUnmasker struct {
	m       *Mapping
	streams map[StreamID]*unmaskStream
	// partial records partial tokens of streams that were already flushed.
	partial bool
}

// NewStreamUnmasker returns a StreamUnmasker for output of an attempt masked
// with m.
func (m *Mapping) NewStreamUnmasker() *StreamUnmasker {
	return &StreamUnmasker{m: m, streams: map[StreamID]*unmaskStream{}}
}

// Text consumes a content delta of choice and returns the text to relay now.
func (s *StreamUnmasker) Text(choice int, delta string) string {
	return s.write(StreamID{Choice: choice, ToolCall: ContentStream}, delta, false)
}

// ToolArgs consumes an arguments delta of tool call callIndex of choice and
// returns the arguments text to relay now; restored values inside JSON string
// literals are JSON-escaped.
func (s *StreamUnmasker) ToolArgs(choice, callIndex int, delta string) string {
	return s.write(StreamID{Choice: choice, ToolCall: callIndex}, delta, true)
}

func (s *StreamUnmasker) write(id StreamID, delta string, jsonMode bool) string {
	u, ok := s.streams[id]
	if !ok {
		if delta == "" {
			return ""
		}
		u = &unmaskStream{m: s.m, json: jsonMode}
		s.streams[id] = u
	}
	return u.write(delta)
}

// Flush ends every open stream and returns the text each still held, ordered
// by choice and then content before tool calls in index order; streams with
// nothing held are omitted. Call it when the upstream stream ends (or is cut)
// and relay the result before finishing the client stream.
func (s *StreamUnmasker) Flush() []Pending {
	return s.flushWhere(func(StreamID) bool { return true })
}

// Partial reports whether a placeholder-like token that is not an exact
// placeholder was seen. It is final once every stream was flushed (by Flush
// or by UnmaskChunk on a finish reason).
func (s *StreamUnmasker) Partial() bool {
	if s.partial {
		return true
	}
	for _, u := range s.streams {
		if u.partial {
			return true
		}
	}
	return false
}

// UnmaskChunk returns a copy of c with its content and tool-call argument
// deltas unmasked. On a choice with a finish reason the streams of that choice
// are flushed into the same chunk: held content is appended to the content
// delta and held arguments to the matching tool-call delta (added, with only
// Index and Arguments set, when the chunk has none for that call). c is not
// modified.
func (s *StreamUnmasker) UnmaskChunk(c chat.Chunk) chat.Chunk {
	out := c
	if c.Choices == nil {
		return out
	}
	out.Choices = make([]chat.ChunkChoice, len(c.Choices))
	for i, ch := range c.Choices {
		ch.Delta.Content = s.Text(ch.Index, ch.Delta.Content)
		if ch.Delta.ToolCalls != nil {
			calls := make([]chat.ToolCallDelta, len(ch.Delta.ToolCalls))
			for j, tc := range ch.Delta.ToolCalls {
				tc.Arguments = s.ToolArgs(ch.Index, tc.Index, tc.Arguments)
				calls[j] = tc
			}
			ch.Delta.ToolCalls = calls
		}
		if ch.FinishReason != "" {
			s.finishChoice(&ch)
		}
		out.Choices[i] = ch
	}
	return out
}

func (s *StreamUnmasker) finishChoice(ch *chat.ChunkChoice) {
	choice := ch.Index
	for _, p := range s.flushWhere(func(id StreamID) bool { return id.Choice == choice }) {
		if p.ToolCall == ContentStream {
			ch.Delta.Content += p.Text
			continue
		}
		found := false
		for j := range ch.Delta.ToolCalls {
			if ch.Delta.ToolCalls[j].Index == p.ToolCall {
				ch.Delta.ToolCalls[j].Arguments += p.Text
				found = true
				break
			}
		}
		if !found {
			ch.Delta.ToolCalls = append(ch.Delta.ToolCalls, chat.ToolCallDelta{Index: p.ToolCall, Arguments: p.Text})
		}
	}
}

func (s *StreamUnmasker) flushWhere(match func(StreamID) bool) []Pending {
	ids := make([]StreamID, 0, len(s.streams))
	for id := range s.streams {
		if match(id) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].Choice != ids[j].Choice {
			return ids[i].Choice < ids[j].Choice
		}
		return ids[i].ToolCall < ids[j].ToolCall
	})
	var out []Pending
	for _, id := range ids {
		u := s.streams[id]
		text := u.flush()
		s.partial = s.partial || u.partial
		delete(s.streams, id)
		if text != "" {
			out = append(out, Pending{StreamID: id, Text: text})
		}
	}
	return out
}
