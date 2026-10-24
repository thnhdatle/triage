// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func streamAll(s *StreamUnmasker, args bool, deltas []string) string {
	var b strings.Builder
	for _, d := range deltas {
		if args {
			b.WriteString(s.ToolArgs(0, 0, d))
		} else {
			b.WriteString(s.Text(0, d))
		}
	}
	for _, p := range s.Flush() {
		b.WriteString(p.Text)
	}
	return b.String()
}

func TestStreamUnmasker_Text_EverySplitOffset(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	inputs := []struct {
		name string
		args bool
		in   string
	}{
		{name: "text", in: "Hi [[EMAIL_1]], [[KR_RRN_1]][[NAME_1]] and [[[EMAIL_2]]] end"},
		{name: "text with partial", in: "x [[EMAIL_1]] [[email_2]] [EMAIL_1] [[EMAIL_7]]"},
		{name: "text with near misses", in: "[[EMAIL_1x [ [[ [[EMAIL_ [[KR_RRN_1]"},
		{name: "arguments", args: true, in: `{"to":"[[EMAIL_1]]","n":"[[NAME_1]]","raw":[[NAME_1]],"k":"\"[[KR_RRN_1]]"}`},
		{name: "arguments with partial", args: true, in: `{"to":"[[ EMAIL_1 ]]","n":"[[NAME_1]]"}`},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var want string
			var wantPartial bool
			if tc.args {
				want, wantPartial = m.UnmaskJSONString(tc.in)
			} else {
				want, wantPartial = m.Unmask(tc.in)
			}
			for i := 0; i <= len(tc.in); i++ {
				for j := i; j <= len(tc.in); j++ {
					s := m.NewStreamUnmasker()
					got := streamAll(s, tc.args, []string{tc.in[:i], tc.in[i:j], tc.in[j:]})
					require.Equal(t, want, got, "split at %d,%d", i, j)
					require.Equal(t, wantPartial, s.Partial(), "split at %d,%d", i, j)
				}
			}
			s := m.NewStreamUnmasker()
			bytes := make([]string, len(tc.in))
			for i := 0; i < len(tc.in); i++ {
				bytes[i] = tc.in[i : i+1]
			}
			assert.Equal(t, want, streamAll(s, tc.args, bytes), "byte by byte")
			assert.Equal(t, wantPartial, s.Partial())
		})
	}
}

func TestStreamUnmasker_Text_HoldBound(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	bound := len("[[KR_RRN_1]]") - 1
	require.Equal(t, bound+1, m.maxKeyLen)
	in := "a [[KR_RRN_1]] b [[EMAIL_2]] [[[[ [[KR_RRN_ [[EMAIL_1]"
	s := m.NewStreamUnmasker()
	var out strings.Builder
	for i := 0; i < len(in); i++ {
		out.WriteString(s.Text(0, in[i:i+1]))
		u := s.streams[StreamID{Choice: 0, ToolCall: ContentStream}]
		require.LessOrEqual(t, len(u.hold), bound)
		assert.True(t, strings.HasSuffix(in[:i+1], u.hold), "only a suffix of the input is held")
	}
	for _, p := range s.Flush() {
		out.WriteString(p.Text)
	}
	assert.Equal(t, "a 900101-1234567 b c@d.org [[[[ [[KR_RRN_ [[EMAIL_1]", out.String())
}

func TestStreamUnmasker_Text_EmitsEarly(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	tests := []struct {
		name     string
		deltas   []string
		want     []string
		wantHeld string
	}{
		{name: "plain text immediately", deltas: []string{"hello"}, want: []string{"hello"}},
		{name: "bracket held", deltas: []string{"a[", "x"}, want: []string{"a", "[x"}},
		{name: "prefix held", deltas: []string{"a[[EMA", "IL_1]]b"}, want: []string{"a", "a@b.comb"}, wantHeld: ""},
		{name: "longest prefix held", deltas: []string{"[[KR_RRN_1]"}, want: []string{""}, wantHeld: "[[KR_RRN_1]"},
		{name: "non prefix bracket", deltas: []string{"[x[[EMAIL_1]] [["}, want: []string{"[xa@b.com "}, wantHeld: "[["},
		{name: "broken prefix released", deltas: []string{"[[EM", "x"}, want: []string{"", "[[EMx"}},
		{name: "empty delta", deltas: []string{"[[", ""}, want: []string{"", ""}, wantHeld: "[["},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := m.NewStreamUnmasker()
			got := make([]string, 0, len(tc.deltas))
			for _, d := range tc.deltas {
				got = append(got, s.Text(3, d))
			}
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantHeld, s.streams[StreamID{Choice: 3, ToolCall: ContentStream}].hold)
		})
	}
}

func TestStreamUnmasker_Partial_AcrossChunks(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	tests := []struct {
		name   string
		deltas []string
		want   bool
	}{
		{name: "lower case split", deltas: []string{"x [[ema", "il_1]] y"}, want: true},
		{name: "unknown index split", deltas: []string{"x [[EMAIL_", "9", "]]"}, want: true},
		{name: "spaces split", deltas: []string{"[[ ", "KR_RRN_1", " ]] done"}, want: true},
		{name: "exact split", deltas: []string{"[[EMA", "IL_1]] . [[KR_RRN", "_1]]"}, want: false},
		{name: "break bytes split tokens", deltas: []string{"[[EMAIL", ".", "_1]]"}, want: false},
		{name: "tail checked on flush", deltas: []string{"ok. [EMAIL_1]"}, want: true},
		{name: "unclosed is no token", deltas: []string{"ok. [EMAIL_1"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := m.NewStreamUnmasker()
			for _, d := range tc.deltas {
				s.Text(0, d)
			}
			s.Flush()
			assert.Equal(t, tc.want, s.Partial())
			assert.Empty(t, s.streams)
		})
	}
}

func TestStreamUnmasker_Partial_BeforeFlush(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()
	s.Text(0, "[[email_1]] then more")
	assert.True(t, s.Partial(), "a completed segment is checked as soon as a break byte arrives")

	s2 := m.NewStreamUnmasker()
	s2.Text(0, "[[email_1]]")
	assert.False(t, s2.Partial(), "an open segment is checked on flush")
	s2.Flush()
	assert.True(t, s2.Partial())

	s3 := NewMapping(userReq("x"), nil).NewStreamUnmasker()
	assert.Equal(t, "[[EMAIL_1]] [[email_1]]", s3.Text(0, "[[EMAIL_1]] [[email_1]]"))
	assert.Empty(t, s3.Flush())
	assert.False(t, s3.Partial())
}

func TestStreamUnmasker_Flush_Order(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()
	assert.Equal(t, `{"a":"`, s.ToolArgs(1, 2, `{"a":"[[EMAIL_`))
	assert.Equal(t, "x", s.ToolArgs(1, 0, `x[`))
	assert.Equal(t, "y", s.Text(1, "y[["))
	assert.Equal(t, "z", s.Text(0, "z[[KR"))
	assert.Equal(t, "done", s.Text(2, "done"))
	assert.Empty(t, s.Text(5, ""), "an empty delta opens no stream")
	_, opened := s.streams[StreamID{Choice: 5, ToolCall: ContentStream}]
	assert.False(t, opened)

	got := s.Flush()
	assert.Equal(t, []Pending{
		{StreamID: StreamID{Choice: 0, ToolCall: ContentStream}, Text: "[[KR"},
		{StreamID: StreamID{Choice: 1, ToolCall: ContentStream}, Text: "[["},
		{StreamID: StreamID{Choice: 1, ToolCall: 0}, Text: "["},
		{StreamID: StreamID{Choice: 1, ToolCall: 2}, Text: `[[EMAIL_`},
	}, got)
	assert.Empty(t, s.streams)
	assert.Nil(t, s.Flush())
}

func TestStreamUnmasker_ToolArgs_StateAcrossDeltas(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()
	parts := []string{`{"a":"x\`, `"`, `[[NAME_1]]","b":`, `[[NAME_1]]}`}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(s.ToolArgs(0, 1, p))
	}
	want := `{"a":"x\"say \"hi\" \\ ok","b":say "hi" \ ok}`
	assert.Equal(t, want, b.String())
	assert.Equal(t, "plain", s.Text(0, "plain"), "content streams are separate")
}

func TestStreamUnmasker_UnmaskChunk_FinishFlushes(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()

	c1 := chat.Chunk{ID: "c", Model: "m", Choices: []chat.ChunkChoice{
		{Index: 0, Delta: chat.Delta{Role: chat.RoleAssistant, Content: "Hi [[EMA"}},
		{Index: 1, Delta: chat.Delta{ToolCalls: []chat.ToolCallDelta{
			{Index: 0, ID: "t0", Type: "function", Name: "send", Arguments: `{"n":"[[NA`},
			{Index: 1, ID: "t1", Name: "other", Arguments: `{"e":"[[EMAIL_2`},
		}}},
	}}
	orig := chat.Chunk{ID: c1.ID, Model: c1.Model, Choices: []chat.ChunkChoice{c1.Choices[0], c1.Choices[1]}}
	orig.Choices[1].Delta.ToolCalls = append([]chat.ToolCallDelta(nil), c1.Choices[1].Delta.ToolCalls...)
	got1 := s.UnmaskChunk(c1)
	assert.Equal(t, orig, c1, "input not modified")
	assert.Equal(t, chat.Chunk{ID: "c", Model: "m", Choices: []chat.ChunkChoice{
		{Index: 0, Delta: chat.Delta{Role: chat.RoleAssistant, Content: "Hi "}},
		{Index: 1, Delta: chat.Delta{ToolCalls: []chat.ToolCallDelta{
			{Index: 0, ID: "t0", Type: "function", Name: "send", Arguments: `{"n":"`},
			{Index: 1, ID: "t1", Name: "other", Arguments: `{"e":"`},
		}}},
	}}, got1)

	c2 := chat.Chunk{ID: "c", Choices: []chat.ChunkChoice{
		{Index: 0, Delta: chat.Delta{Content: "IL_1]] [["}, FinishReason: chat.FinishStop},
		{
			Index:        1,
			Delta:        chat.Delta{ToolCalls: []chat.ToolCallDelta{{Index: 0, Arguments: `ME_1]]"} [`}}},
			FinishReason: chat.FinishToolCalls,
		},
	}}
	got2 := s.UnmaskChunk(c2)
	assert.Equal(t, chat.Chunk{ID: "c", Choices: []chat.ChunkChoice{
		{Index: 0, Delta: chat.Delta{Content: "a@b.com [["}, FinishReason: chat.FinishStop},
		{Index: 1, Delta: chat.Delta{ToolCalls: []chat.ToolCallDelta{
			{Index: 0, Arguments: `say \"hi\" \\ ok"} [`},
			{Index: 1, Arguments: `[[EMAIL_2`},
		}}, FinishReason: chat.FinishToolCalls},
	}}, got2)
	assert.Empty(t, s.streams)
	assert.False(t, s.Partial())
	assert.Nil(t, s.Flush())
}

func TestStreamUnmasker_UnmaskChunk_Passthrough(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()
	usage := &chat.Usage{PromptTokens: 1}
	c := chat.Chunk{ID: "u", Usage: usage}
	got := s.UnmaskChunk(c)
	assert.Equal(t, c, got)
	assert.Nil(t, got.Choices)

	empty := chat.Chunk{Choices: []chat.ChunkChoice{}}
	assert.Equal(t, empty, s.UnmaskChunk(empty))

	fin := s.UnmaskChunk(chat.Chunk{Choices: []chat.ChunkChoice{{Index: 0, FinishReason: chat.FinishLength}}})
	assert.Equal(t, chat.Chunk{Choices: []chat.ChunkChoice{{Index: 0, FinishReason: chat.FinishLength}}}, fin)
	assert.Nil(t, fin.Choices[0].Delta.ToolCalls)
}

func TestStreamUnmasker_UnmaskChunk_FinishOnlyFlushesItsChoice(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()
	s.UnmaskChunk(chat.Chunk{Choices: []chat.ChunkChoice{
		{Index: 0, Delta: chat.Delta{Content: "a [["}},
		{Index: 1, Delta: chat.Delta{Content: "b [email_1] [[EMA"}},
	}})
	got := s.UnmaskChunk(chat.Chunk{Choices: []chat.ChunkChoice{{Index: 1, FinishReason: chat.FinishStop}}})
	assert.Equal(t, "[[EMA", got.Choices[0].Delta.Content)
	assert.True(t, s.Partial(), "partial of a flushed choice is kept")
	assert.Equal(t, []Pending{{StreamID: StreamID{Choice: 0, ToolCall: ContentStream}, Text: "[["}}, s.Flush())
	assert.True(t, s.Partial())
}

func TestStreamUnmasker_Partial_SplitterAtDeltaStart(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()
	s.Text(0, "[[email_1]]")
	require.False(t, s.Partial())
	s.Text(0, ".")
	assert.True(t, s.Partial(), "the first byte of a delta closes the segment")
}

func TestStreamUnmasker_Text_NonASCIIBytes(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	s := m.NewStreamUnmasker()
	in := "Ѐ[[EMAIL_1]]Ѐ [[KR_RRN_1]]"
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		b.WriteString(s.Text(0, in[i:i+1]))
	}
	for _, p := range s.Flush() {
		b.WriteString(p.Text)
	}
	assert.Equal(t, "Ѐa@b.comЀ [[KR_RRN_1]]", b.String())
	assert.True(t, s.Partial())
}
