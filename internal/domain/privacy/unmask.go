// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"bytes"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// Unmask restores every exact placeholder of the mapping in text produced by
// a masked attempt. partial reports a placeholder-like token
// ((?i)\[{1,2}\s*<TYPE>_\d+\s*\]{1,2} for a TYPE of the mapping) that is not an
// exact placeholder of the mapping, e.g. "[[email_1]]", "[EMAIL_1]" or an
// unknown index; such tokens are returned as produced. Placeholders absent
// from text are not partial.
func (m *Mapping) Unmask(text string) (out string, partial bool) { return m.unmask(text, false) }

// UnmaskJSONString is Unmask for tool-call arguments, which are JSON text: a
// placeholder inside a JSON string literal is restored with the value
// JSON-string-escaped ('"' as \", '\' as \\, control characters escaped, no
// HTML escaping); a placeholder outside string literals is restored verbatim.
func (m *Mapping) UnmaskJSONString(text string) (out string, partial bool) {
	return m.unmask(text, true)
}

func (m *Mapping) unmask(text string, jsonMode bool) (string, bool) {
	u := unmaskStream{m: m, json: jsonMode}
	out := u.write(text) + u.flush()
	return out, u.partial
}

// UnmaskResponse returns a copy of resp with the text parts and tool-call
// arguments of every choice unmasked, and whether any of them was partial.
// resp is not modified.
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

// unmaskStream restores placeholders in one output stream (a choice's content
// or one tool call's arguments). It holds back at most maxKeyLen-1 bytes: the
// shortest suffix that is a proper prefix of a placeholder.
type unmaskStream struct {
	m    *Mapping
	json bool
	// hold is the pending suffix that may begin a placeholder.
	hold string
	// inString and escaped track the JSON lexer state of the emitted upstream
	// bytes (json mode only); placeholders contain neither '"' nor '\'.
	inString, escaped bool
	// scan is the upstream text since the last byte that cannot occur in a
	// partial-pattern match; it is checked when such a byte arrives or on flush.
	scan    []byte
	partial bool
}

// write consumes an upstream delta and returns the bytes that can be emitted.
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

// flush returns the held bytes and completes partial detection.
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

// observe feeds upstream bytes to partial detection. A partial-pattern match
// never spans a byte outside the token byte set, so the text before the last
// such byte can be checked now and dropped, with the same result as checking
// the reassembled stream.
func (u *unmaskStream) observe(delta string) {
	if len(u.m.tokens) == 0 {
		return
	}
	old := len(u.scan)
	u.scan = append(u.scan, delta...)
	// Only the new bytes can hold a splitter: the old ones were cut at one.
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
	// Every partial-pattern match holds a '['; without one the (lazily
	// compiled) pattern is not needed.
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

// keyAt returns the placeholder rest starts with, if any. A placeholder's
// body has no brackets, so its first ']' is followed by its closing ']'.
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

// isKeyPrefix reports whether rest is a proper prefix of a placeholder. The
// length check keeps the lookup O(maxKeyLen) instead of hashing all of rest.
func (m *Mapping) isKeyPrefix(rest string) bool {
	if len(rest) >= m.maxKeyLen {
		return false
	}
	_, ok := m.prefixes[rest]
	return ok
}
