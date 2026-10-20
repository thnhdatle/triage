// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// fuzzFindings derives up to 8 findings from picks: each pair of bytes selects
// a start offset and a length (1..12) of a span of text.
func fuzzFindings(text string, picks []byte) []Finding {
	types := []string{"email", "phone", "kr_rrn", "credit_card"}
	var out []Finding
	for i := 0; i+1 < len(picks) && len(out) < 8 && text != ""; i += 2 {
		start := int(picks[i]) % len(text)
		end := min(start+int(picks[i+1])%12+1, len(text))
		out = append(out, finding(types[len(out)%len(types)], text[start:end]))
	}
	return out
}

// survivable reports whether a value may legitimately reappear inside or
// around a placeholder: it contains a bracket or only placeholder body bytes.
func survivable(v string) bool {
	return strings.ContainsAny(v, "[]") || strings.Trim(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") == ""
}

func FuzzMapping_RoundTrip(f *testing.F) {
	f.Add("mail a@b.com or a@b.com", []byte{5, 6})
	f.Fuzz(func(t *testing.T, text string, picks []byte) {
		req := chat.Request{Messages: []chat.Message{
			chat.TextMessage(chat.RoleSystem, text),
			chat.TextMessage(chat.RoleUser, text),
			toolCallMsg(text),
		}}
		findings := fuzzFindings(text, picks)
		m := NewMapping(req, findings)
		masked := m.MaskRequest(req)
		maskedText := masked.Messages[1].Content[0].Text
		require.Equal(t, maskedText, masked.Messages[0].Content[0].Text)

		back, _ := m.Unmask(maskedText)
		require.Equal(t, text, back)

		cut1 := len(picks) % (len(maskedText) + 1)
		cut2 := cut1 + (len(text) % (len(maskedText) - cut1 + 1))
		s := m.NewStreamUnmasker()
		var streamed strings.Builder
		for _, d := range []string{maskedText[:cut1], maskedText[cut1:cut2], maskedText[cut2:]} {
			streamed.WriteString(s.Text(0, d))
		}
		for _, p := range s.Flush() {
			streamed.WriteString(p.Text)
		}
		require.Equal(t, text, streamed.String())

		maskedArgs := masked.Messages[2].ToolCalls[0].Function.Arguments
		if json.Valid([]byte(text)) {
			require.True(t, json.Valid([]byte(maskedArgs)), maskedArgs)
		}
		_, _ = m.UnmaskJSONString(maskedArgs)

		for _, fd := range findings {
			if !survivable(fd.Value) {
				require.NotContains(t, maskedText, fd.Value)
			}
		}
	})
}
