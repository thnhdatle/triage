// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// MaskRequest returns a deep copy of req for a cloud attempt in which every
// occurrence of every mapped value (not only the detected spans) is replaced
// by its placeholder, in the text parts and tool-call arguments of every
// message. System messages are masked even when they were not scanned, so a
// value found elsewhere never reaches a cloud target through the system
// prompt. Image parts, names, tool definitions and pass-through fields are not
// touched. req is not modified.
//
// Text parts are masked in one left-to-right pass; at each position the
// longest matching value wins, so overlapping values are handled
// deterministically and inserted placeholders are never re-matched.
// Tool-call arguments that are valid JSON stay valid JSON; see maskJSON.
func (m *Mapping) MaskRequest(req chat.Request) chat.Request {
	out := req.Clone()
	if m.Empty() {
		return out
	}
	for i := range out.Messages {
		msg := &out.Messages[i]
		for j := range msg.Content {
			if msg.Content[j].Type == chat.PartText {
				msg.Content[j].Text = m.maskText(msg.Content[j].Text)
			}
		}
		for j := range msg.ToolCalls {
			msg.ToolCalls[j].Function.Arguments = m.maskJSON(msg.ToolCalls[j].Function.Arguments)
		}
	}
	return out
}

// maskText replaces every value in s by its placeholder.
func (m *Mapping) maskText(s string) string {
	if m.textReplacer == nil {
		return s
	}
	return m.textReplacer.Replace(s)
}

// maskJSON masks tool-call arguments. Both the raw and the JSON-escaped form of
// each value are replaced. When s is valid JSON:
//   - inside a string literal a replacement is widened to whole escape
//     sequences and UTF-8 sequences, so it never splits "\n" or "\u00e9";
//   - a bare token (number, true, false, null) containing a value becomes a
//     string literal holding the masked token, e.g. 4111111111111111 becomes
//     "[[CREDIT_CARD_1]]".
//
// Invalid JSON is masked as plain text.
func (m *Mapping) maskJSON(s string) string {
	if m.argReplacer == nil || m.argReplacer.Replace(s) == s {
		return s
	}
	if !json.Valid([]byte(s)) {
		return m.argReplacer.Replace(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			end := stringEnd(s, i)
			b.WriteByte('"')
			b.WriteString(m.maskJSONContent(s[i+1 : end]))
			b.WriteByte('"')
			i = end + 1
		case isJSONDelim(c):
			b.WriteByte(c)
			i++
		default:
			j := i
			for j < len(s) && !isJSONDelim(s[j]) {
				j++
			}
			tok := s[i:j]
			if masked := m.argReplacer.Replace(tok); masked != tok {
				b.WriteByte('"')
				b.WriteString(masked)
				b.WriteByte('"')
			} else {
				b.WriteString(tok)
			}
			i = j
		}
	}
	return b.String()
}

// isJSONDelim reports whether c ends a bare JSON token.
func isJSONDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '{', '}', '[', ']', ':', ',', '"':
		return true
	}
	return false
}

// stringEnd returns the index of the quote closing the string literal opening
// at s[i]; s must be valid JSON.
func stringEnd(s string, i int) int {
	j := i + 1
	for s[j] != '"' {
		if s[j] == '\\' {
			j++
		}
		j++
	}
	return j
}

// maskJSONContent masks the raw content of one JSON string literal.
func (m *Mapping) maskJSONContent(c string) string {
	if m.argReplacer.Replace(c) == c {
		return c
	}
	start, end := jsonUnits(c)
	var b strings.Builder
	b.Grow(len(c))
	last := 0
	for i := 0; i < len(c); {
		f, ok := m.argFormAt(c[i:])
		if !ok {
			i++
			continue
		}
		b.WriteString(c[last:start[i]])
		b.WriteString(f.placeholder)
		last = end[i+len(f.old)-1]
		i = last
	}
	b.WriteString(c[last:])
	return b.String()
}

// argFormAt returns the longest form that rest starts with.
func (m *Mapping) argFormAt(rest string) (form, bool) {
	for _, f := range m.argForms[rest[0]] {
		if strings.HasPrefix(rest, f.old) {
			return f, true
		}
	}
	return form{}, false
}

// jsonUnits returns, for every byte of a valid JSON string content, the start
// and end of its unit: an escape sequence, a UTF-8 sequence or a single byte.
func jsonUnits(c string) (start, end []int) {
	start = make([]int, len(c))
	end = make([]int, len(c))
	for i := 0; i < len(c); {
		n := 1
		switch {
		case c[i] == '\\' && c[i+1] == 'u':
			n = 6
		case c[i] == '\\':
			n = 2
		case c[i] >= utf8.RuneSelf:
			_, n = utf8.DecodeRuneInString(c[i:])
		}
		for k := i; k < i+n; k++ {
			start[k] = i
			end[k] = i + n
		}
		i += n
	}
	return start, end
}
