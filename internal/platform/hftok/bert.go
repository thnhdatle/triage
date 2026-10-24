// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

type bertNormalizer struct {
	cleanText    bool
	chineseChars bool
}

func newBertNormalizer(raw json.RawMessage) (*bertNormalizer, error) {
	var cfg struct {
		CleanText          bool  `json:"clean_text"`
		HandleChineseChars bool  `json:"handle_chinese_chars"`
		StripAccents       *bool `json:"strip_accents"`
		Lowercase          bool  `json:"lowercase"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%w: BertNormalizer: %w", ErrInvalid, err)
	}
	strip := cfg.Lowercase
	if cfg.StripAccents != nil {
		strip = *cfg.StripAccents
	}
	if cfg.Lowercase || strip {
		return nil, fmt.Errorf("%w: BertNormalizer lowercase/strip_accents", ErrUnsupported)
	}
	return &bertNormalizer{cleanText: cfg.CleanText, chineseChars: cfg.HandleChineseChars}, nil
}

func isChineseChar(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, r >= 0x3400 && r <= 0x4DBF,
		r >= 0x20000 && r <= 0x2A6DF, r >= 0x2A700 && r <= 0x2B73F,
		r >= 0x2B740 && r <= 0x2B81F, r >= 0x2B920 && r <= 0x2CEAF,
		r >= 0xF900 && r <= 0xFAFF, r >= 0x2F800 && r <= 0x2FA1F:
		return true
	default:
		return false
	}
}

func (n *bertNormalizer) normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if n.cleanText {
			if unicode.Is(bertRemoved, r) {
				continue
			}
			if unicode.IsSpace(r) {
				r = ' '
			}
		}
		if n.chineseChars && isChineseChar(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

type bertPreTokenizer struct{}

func isBertPunct(r rune) bool {
	return unicode.Is(bertPunct, r)
}

func (bertPreTokenizer) split(words []string) []string {
	out := make([]string, 0, len(words)*2)
	for _, w := range words {
		start := 0
		for i, r := range w {
			switch {
			case unicode.IsSpace(r):
				if start < i {
					out = append(out, w[start:i])
				}
				start = i + len(string(r))
			case isBertPunct(r):
				if start < i {
					out = append(out, w[start:i])
				}
				end := i + len(string(r))
				out = append(out, w[i:end])
				start = end
			}
		}
		if start < len(w) {
			out = append(out, w[start:])
		}
	}
	return out
}
