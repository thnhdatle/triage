// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"bytes"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func (m *Mapping) Unmask(text string) (out string, partial bool) { return m.unmask(text, false) }

func (m *Mapping) UnmaskJSONString(text string) (out string, partial bool) {
	return m.unmask(text, true)
}

func (m *Mapping) unmask(text string, jsonMode bool) (string, bool) {
	u := unmaskStream{m: m, json: jsonMode}
	out := u.write(text) + u.flush()
	return out, u.partial
}

// UnmaskResponse restores the original values in resp.
func (m *Mapping) UnmaskResponse(resp chat.Response) (chat.Response, bool) {
	out := resp
	out.Choices = make([]chat.Choice, len(resp.Choices))
	partial := false
	for i, c := range resp.Choices {
		var pc, pt bool
		c.Message.Content, pc = m.unmaskParts(c.Message.Content)
		c.Message.ToolCalls, pt = m.unmaskToolCalls(c.Message.ToolCalls)
		partial = partial || pc || pt
		out.Choices[i] = c
	}
	return out, partial
}

func (m *Mapping) unmaskParts(parts []chat.Part) ([]chat.Part, bool) {
	if parts == nil {
		return nil, false
	}
	out := make([]chat.Part, len(parts))
	partial := false
	for i, p := range parts {
		if p.Type == chat.PartText {
			var pp bool
			p.Text, pp = m.Unmask(p.Text)
			partial = partial || pp
		}
		out[i] = p
	}
	return out, partial
}

func (m *Mapping) unmaskToolCalls(calls []chat.ToolCall) ([]chat.ToolCall, bool) {
	if calls == nil {
		return nil, false
	}
	out := make([]chat.ToolCall, len(calls))
	partial := false
	for i, tc := range calls {
		var pp bool
		tc.Function.Arguments, pp = m.UnmaskJSONString(tc.Function.Arguments)
		partial = partial || pp
		out[i] = tc
	}
	return out, partial
}

type unmaskStream struct {
	m                 *Mapping
	json              bool
	hold              string
	inString, escaped bool
	scan              []byte
	partial           bool
}

func (u *unmaskStream) write(delta string) string {
	u.observe(delta)
	buf := u.hold + delta
	u.hold = ""
	var b strings.Builder
	b.Grow(len(buf))
	for i := 0; i < len(buf); {
		j := strings.IndexByte(buf[i:], '[')
		if j < 0 {
			u.emit(&b, buf[i:])
			break
		}
		u.emit(&b, buf[i:i+j])
		i += j
		rest := buf[i:]
		if k, ok := u.m.keyAt(rest); ok {
			b.WriteString(u.restore(u.m.keys[k]))
			i += len(k)
			continue
		}
		if u.m.isKeyPrefix(rest) {
			u.hold = rest
			break
		}
		u.emit(&b, "[")
		i++
	}
	return b.String()
}

func (u *unmaskStream) flush() string {
	var b strings.Builder
	u.emit(&b, u.hold)
	u.hold = ""
	u.check(u.scan)
	u.scan = nil
	return b.String()
}

func (u *unmaskStream) emit(b *strings.Builder, s string) {
	b.WriteString(s)
	if !u.json {
		return
	}
	for i := 0; i < len(s); i++ {
		switch {
		case u.escaped:
			u.escaped = false
		case s[i] == '\\':
			u.escaped = u.inString
		case s[i] == '"':
			u.inString = !u.inString
		}
	}
}

func (u *unmaskStream) restore(v string) string {
	if u.json && u.inString {
		return jsonEscape(v)
	}
	return v
}

func (u *unmaskStream) observe(delta string) {
	if len(u.m.tokens) == 0 {
		return
	}
	old := len(u.scan)
	u.scan = append(u.scan, delta...)
	for i := len(u.scan) - 1; i >= old; i-- {
		c := u.scan[i]
		if int(c) < len(u.m.tokenBytes) && !u.m.tokenBytes[c] {
			u.check(u.scan[:i])
			u.scan = append(u.scan[:0], u.scan[i+1:]...)
			return
		}
	}
}

func (u *unmaskStream) check(seg []byte) {
	if len(u.m.tokens) == 0 || u.partial || bytes.IndexByte(seg, '[') < 0 {
		return
	}
	for _, t := range u.m.partialRe().FindAll(seg, -1) {
		if _, ok := u.m.keys[string(t)]; !ok {
			u.partial = true
			return
		}
	}
}

func (m *Mapping) keyAt(rest string) (string, bool) {
	window := rest[:min(len(rest), m.maxKeyLen)]
	end := strings.IndexByte(window, ']')
	if end < 0 || end+1 >= len(window) || window[end+1] != ']' {
		return "", false
	}
	k := window[:end+2]
	_, ok := m.keys[k]
	return k, ok
}

func (m *Mapping) isKeyPrefix(rest string) bool {
	if len(rest) >= m.maxKeyLen {
		return false
	}
	_, ok := m.prefixes[rest]
	return ok
}
