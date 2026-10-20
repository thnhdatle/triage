// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Placeholder grammar: "[[" TYPE "_" n "]]", TYPE in [A-Z0-9_]+,
// n a decimal index from 1. Every byte is ASCII and none is altered by JSON or
// HTML escaping.
const (
	placeholderOpen  = "[["
	placeholderClose = "]]"
	// fallbackTypeToken is the TYPE of findings whose type is empty.
	fallbackTypeToken = "PII"
	hexDigits         = "0123456789abcdef"
)

// reservedPattern matches every string that has the exact placeholder grammar.
// Two such strings can never overlap (the body has no brackets), so
// FindAllString returns every occurrence.
var reservedPattern = regexp.MustCompile(`\[\[[A-Z0-9_]+_[0-9]+\]\]`)

// Placeholder returns the placeholder of the n-th (n >= 1) distinct value of a
// finding type: Placeholder("credit_card", 2) == "[[CREDIT_CARD_2]]". ASCII
// letters are upper-cased, every other byte outside [A-Za-z0-9_] becomes '_'
// and an empty type becomes "PII".
func Placeholder(findingType string, n int) string {
	return placeholderFor(typeToken(findingType), n)
}

func placeholderFor(token string, n int) string {
	return placeholderOpen + token + "_" + strconv.Itoa(n) + placeholderClose
}

// typeToken maps a finding type to the TYPE part of its placeholders.
func typeToken(findingType string) string {
	if findingType == "" {
		return fallbackTypeToken
	}
	b := []byte(findingType)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - ('a' - 'A')
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}

// jsonEscape returns s escaped as the content of a JSON string literal: '"',
// '\' and control characters are escaped; nothing is HTML-escaped and all other
// bytes (including invalid UTF-8 and U+2028/U+2029) are kept as they are.
func jsonEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// partialPattern compiles the partial-unmask pattern
// (?i)\[{1,2}\s*<TYPE>_\d+\s*\]{1,2} for the given type tokens, or returns nil
// when there are none.
func partialPattern(tokens map[string]struct{}) *regexp.Regexp {
	if len(tokens) == 0 {
		return nil
	}
	alts := make([]string, 0, len(tokens))
	for t := range tokens {
		alts = append(alts, regexp.QuoteMeta(t))
	}
	sort.Slice(alts, func(i, j int) bool {
		if len(alts[i]) != len(alts[j]) {
			return len(alts[i]) > len(alts[j])
		}
		return alts[i] < alts[j]
	})
	return regexp.MustCompile(`(?i)\[{1,2}\s*(?:` + strings.Join(alts, "|") + `)_\d+\s*\]{1,2}`)
}

// tokenByteSet returns the ASCII bytes that can occur in a match of the
// partial pattern for the given tokens: brackets, '_', digits, RE2 \s and the
// token letters in both cases. Any other ASCII byte splits the text into
// independent segments for partial detection. Non-ASCII bytes are never
// treated as splitters (case folding maps e.g. U+212A KELVIN SIGN to 'k').
func tokenByteSet(tokens map[string]struct{}) [utf8.RuneSelf]bool {
	var set [utf8.RuneSelf]bool
	for _, c := range []byte("[]_0123456789\t\n\f\r ") {
		set[c] = true
	}
	for t := range tokens {
		for i := 0; i < len(t); i++ {
			c := t[i]
			if c >= 'A' && c <= 'Z' {
				set[c] = true
				set[c+('a'-'A')] = true
			}
		}
	}
	return set
}
