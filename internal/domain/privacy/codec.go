// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	placeholderOpen   = "[["
	placeholderClose  = "]]"
	fallbackTypeToken = "PII"
	hexDigits         = "0123456789abcdef"
)

var reservedPattern = regexp.MustCompile(`\[\[[A-Z0-9_]+_[0-9]+\]\]`)

func Placeholder(findingType string, n int) string {
	return placeholderFor(typeToken(findingType), n)
}

func placeholderFor(token string, n int) string {
	return placeholderOpen + token + "_" + strconv.Itoa(n) + placeholderClose
}

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
