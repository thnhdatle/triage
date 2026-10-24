// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func propValue() *rapid.Generator[string] {
	return rapid.StringMatching(`[g-mopqsv-z][g-z0-9A-Z_ @."\\é]{0,8}[g-mopqsv-z]`)
}

func propFiller() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-zA-Z0-9_ @."\\\[\]\n]{0,10}`)
}

func propTypes() []string { return []string{"email", "name", "kr_rrn"} }

func propRoles() []chat.Role {
	return []chat.Role{chat.RoleSystem, chat.RoleUser, chat.RoleAssistant, chat.RoleTool}
}

func propTokens() []string {
	return []string{"[[EMAIL_1]]", "[[NAME_2]]", "[[KR_RRN_1]]", "[[", "]]", "[email_1]", "_1"}
}

func propText(t *rapid.T, values []string, label string) string {
	var b strings.Builder
	n := rapid.IntRange(0, 6).Draw(t, label+"_pieces")
	for i := 0; i < n; i++ {
		switch rapid.IntRange(0, 2).Draw(t, label+"_kind") {
		case 0:
			b.WriteString(rapid.SampledFrom(values).Draw(t, label+"_value"))
		case 1:
			b.WriteString(rapid.SampledFrom(propTokens()).Draw(t, label+"_token"))
		default:
			b.WriteString(propFiller().Draw(t, label+"_filler"))
		}
	}
	return b.String()
}

func propArgs(t *rapid.T, values []string) string {
	items := make([]string, rapid.IntRange(1, 3).Draw(t, "args_len"))
	for i := range items {
		items[i] = propText(t, values, "arg")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(items); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func propRequest(t *rapid.T, values []string) chat.Request {
	var req chat.Request
	for i := rapid.IntRange(1, 4).Draw(t, "messages"); i > 0; i-- {
		msg := chat.Message{Role: rapid.SampledFrom(propRoles()).Draw(t, "role")}
		for j := rapid.IntRange(1, 2).Draw(t, "parts"); j > 0; j-- {
			msg.Content = append(msg.Content, chat.Part{Type: chat.PartText, Text: propText(t, values, "part")})
		}
		if rapid.Bool().Draw(t, "tool_call") {
			msg.ToolCalls = []chat.ToolCall{{ID: "c", Type: "function", Function: chat.FunctionCall{
				Name: "f", Arguments: propArgs(t, values),
			}}}
		}
		req.Messages = append(req.Messages, msg)
	}
	return req
}

func streamSplit(t *rapid.T, m *Mapping, s string, args bool) string {
	u := m.NewStreamUnmasker()
	var b strings.Builder
	for rest := s; rest != ""; {
		n := rapid.IntRange(1, len(rest)).Draw(t, "chunk")
		if args {
			b.WriteString(u.ToolArgs(0, 0, rest[:n]))
		} else {
			b.WriteString(u.Text(0, rest[:n]))
		}
		rest = rest[n:]
	}
	for _, p := range u.Flush() {
		b.WriteString(p.Text)
	}
	return b.String()
}

func TestMapping_MaskRequest_PropertyNoValueSurvivesAndRoundTrips(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		values := rapid.SliceOfNDistinct(propValue(), 1, 5, rapid.ID[string]).Draw(rt, "values")
		findings := make([]Finding, len(values))
		hasBackslash := false
		for i, v := range values {
			findings[i] = finding(rapid.SampledFrom(propTypes()).Draw(rt, "type"), v)
			hasBackslash = hasBackslash || strings.Contains(v, `\`)
		}
		req := propRequest(rt, values)
		orig := req.Clone()
		m := NewMapping(req, findings)
		masked := m.MaskRequest(req)
		require.Equal(rt, orig, req, "input not modified")

		var texts, args strings.Builder
		for i, msg := range masked.Messages {
			for j, p := range msg.Content {
				texts.WriteString(p.Text + "\x00")
				back, _ := m.Unmask(p.Text)
				require.Equal(rt, req.Messages[i].Content[j].Text, back)
				require.Equal(rt, back, streamSplit(rt, m, p.Text, false))
			}
			for j, tc := range msg.ToolCalls {
				a := tc.Function.Arguments
				args.WriteString(a + "\x00")
				require.True(rt, json.Valid([]byte(a)), a)
				back, _ := m.UnmaskJSONString(a)
				require.Equal(rt, req.Messages[i].ToolCalls[j].Function.Arguments, back)
				require.Equal(rt, back, streamSplit(rt, m, a, true))
			}
		}
		for _, v := range values {
			require.NotContains(rt, texts.String(), v)
			require.NotContains(rt, args.String(), jsonEscape(v))
			if !strings.ContainsAny(v, `"\`) {
				require.NotContains(rt, args.String(), v)
			}
		}
		if !hasBackslash {
			require.False(rt, m.Leaks(masked))
		}
	})
}
