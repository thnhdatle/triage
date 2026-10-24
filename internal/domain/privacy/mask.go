// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// MaskRequest replaces the found values in req with placeholders.
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

func (m *Mapping) maskText(s string) string {
	if m.textReplacer == nil {
		return s
	}
	return m.textReplacer.Replace(s)
}

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

func isJSONDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '{', '}', '[', ']', ':', ',', '"':
		return true
	}
	return false
}

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

func (m *Mapping) argFormAt(rest string) (form, bool) {
	for _, f := range m.argForms[rest[0]] {
		if strings.HasPrefix(rest, f.old) {
			return f, true
		}
	}
	return form{}, false
}

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
