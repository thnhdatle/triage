// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaceholder_Format_Grammar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		typ  string
		n    int
		want string
	}{
		{name: "email", typ: "email", n: 1, want: "[[EMAIL_1]]"},
		{name: "snake case", typ: "credit_card", n: 2, want: "[[CREDIT_CARD_2]]"},
		{name: "digits kept", typ: "kr_rrn9", n: 10, want: "[[KR_RRN9_10]]"},
		{name: "already upper", typ: "IBAN", n: 3, want: "[[IBAN_3]]"},
		{name: "boundary letters", typ: "azAZ09_", n: 1, want: "[[AZAZ09__1]]"},
		{name: "empty type", typ: "", n: 1, want: "[[PII_1]]"},
		{name: "punctuation", typ: "a-b.c[d]", n: 1, want: "[[A_B_C_D__1]]"},
		{name: "non ascii", typ: "é", n: 1, want: "[[___1]]"},
		{name: "byte after z and before a", typ: "{`@[", n: 1, want: "[[_____1]]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Placeholder(tc.typ, tc.n)
			assert.Equal(t, tc.want, got)
			assert.True(t, reservedPattern.MatchString(got))
			raw, err := json.Marshal(got)
			require.NoError(t, err)
			assert.Equal(t, `"`+got+`"`, string(raw), "JSON and HTML escaping leave a placeholder unchanged")
		})
	}
}

func TestJSONEscape_Escape_Bytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "a@b.com", want: "a@b.com"},
		{name: "quote", in: `say "hi"`, want: `say \"hi\"`},
		{name: "backslash", in: `C:\dir`, want: `C:\\dir`},
		{name: "newline", in: "a\nb", want: `a\nb`},
		{name: "carriage return", in: "a\rb", want: `a\rb`},
		{name: "tab", in: "a\tb", want: `a\tb`},
		{name: "backspace", in: "a\bb", want: `a\bb`},
		{name: "form feed", in: "a\fb", want: `a\fb`},
		{name: "nul", in: "\x00", want: `\u0000`},
		{name: "control 0x01", in: "\x01", want: `\u0001`},
		{name: "control 0x1b", in: "\x1b", want: `\u001b`},
		{name: "control 0x1f", in: "\x1f", want: `\u001f`},
		{name: "space kept", in: " ", want: " "},
		{name: "no html escaping", in: "<a>&", want: "<a>&"},
		{name: "del kept", in: "\x7f", want: "\x7f"},
		{name: "line separator kept", in: "\u2028", want: "\u2028"},
		{name: "utf8 kept", in: "é한", want: "é한"},
		{name: "invalid utf8 kept", in: "\xff", want: "\xff"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := jsonEscape(tc.in)
			assert.Equal(t, tc.want, got)
			if tc.name == "invalid utf8 kept" {
				return
			}
			var back string
			require.NoError(t, json.Unmarshal([]byte(`"`+got+`"`), &back))
			assert.Equal(t, tc.in, back)
		})
	}
}

func TestPartialPattern_Compile_Tokens(t *testing.T) {
	t.Parallel()
	assert.Nil(t, partialPattern(nil))
	assert.Nil(t, partialPattern(map[string]struct{}{}))

	re := partialPattern(map[string]struct{}{"KR": {}, "KR_RRN": {}, "A.B": {}})
	require.NotNil(t, re)
	assert.Equal(t, `(?i)\[{1,2}\s*(?:KR_RRN|A\.B|KR)_\d+\s*\]{1,2}`, re.String())
	tests := []struct {
		in   string
		want string
	}{
		{in: "x [[KR_RRN_1]] y", want: "[[KR_RRN_1]]"},
		{in: "[ kr_7 ]", want: "[ kr_7 ]"},
		{in: "[[A.B_1]]", want: "[[A.B_1]]"},
		{in: "[[AxB_1]]", want: ""},
		{in: "[[KR_]]", want: ""},
		{in: "[[[KR_1]]]", want: "[[KR_1]]"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, re.FindString(tc.in), tc.in)
	}
}

func TestTokenByteSet_Build_Bytes(t *testing.T) {
	t.Parallel()
	set := tokenByteSet(map[string]struct{}{"AZ_9": {}})
	for _, c := range []byte("[]_0123456789\t\n\f\r AZaz") {
		assert.True(t, set[c], "%q is a token byte", c)
	}
	for _, c := range []byte("BYby@.\v-{}`") {
		assert.False(t, set[c], "%q splits", c)
	}
}
