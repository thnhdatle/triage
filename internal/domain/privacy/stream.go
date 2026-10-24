// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"sort"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

const ContentStream = -1

type StreamID struct {
	Choice   int
	ToolCall int
}

type Pending struct {
	StreamID
	Text string
}

// StreamUnmasker restores values in a streamed response.
type StreamUnmasker struct {
	m       *Mapping
	streams map[StreamID]*unmaskStream
	partial bool
}

func (m *Mapping) NewStreamUnmasker() *StreamUnmasker {
	return &StreamUnmasker{m: m, streams: map[StreamID]*unmaskStream{}}
}

func (s *StreamUnmasker) Text(choice int, delta string) string {
	return s.write(StreamID{Choice: choice, ToolCall: ContentStream}, delta, false)
}

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

func (s *StreamUnmasker) Flush() []Pending {
	return s.flushWhere(func(StreamID) bool { return true })
}

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
