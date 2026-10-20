// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// whitespaceSplit splits on Unicode White_Space (Rust char::is_whitespace,
// which equals Go's unicode.IsSpace), dropping the whitespace.
type whitespaceSplit struct{}

func (whitespaceSplit) split(words []string) []string {
	out := make([]string, 0, len(words)*4)
	for _, w := range words {
		out = append(out, strings.FieldsFunc(w, unicode.IsSpace)...)
	}
	return out
}

// metaspace ports HF Metaspace with prepend_scheme "always" or "never": spaces
// become the replacement, the replacement is prepended unless the word
// already starts with it, and with split the word is cut before every
// replacement (MergedWithNext).
type metaspace struct {
	replacement string
	prepend     bool
	splitWords  bool
}

func newMetaspace(raw json.RawMessage) (*metaspace, error) {
	var cfg struct {
		Replacement    string `json:"replacement"`
		PrependScheme  string `json:"prepend_scheme"`
		AddPrefixSpace *bool  `json:"add_prefix_space"`
		Split          *bool  `json:"split"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%w: Metaspace: %w", ErrInvalid, err)
	}
	if utf8.RuneCountInString(cfg.Replacement) != 1 {
		return nil, fmt.Errorf("%w: Metaspace replacement %q", ErrInvalid, cfg.Replacement)
	}
	m := &metaspace{replacement: cfg.Replacement, splitWords: cfg.Split == nil || *cfg.Split}
	switch {
	case cfg.PrependScheme == "always":
		m.prepend = true
	case cfg.PrependScheme == "never":
	case cfg.PrependScheme == "" && cfg.AddPrefixSpace != nil:
		m.prepend = *cfg.AddPrefixSpace
	default:
		return nil, fmt.Errorf("%w: Metaspace prepend_scheme %q", ErrUnsupported, cfg.PrependScheme)
	}
	return m, nil
}

func (m *metaspace) split(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.ReplaceAll(w, " ", m.replacement)
		if m.prepend && !strings.HasPrefix(w, m.replacement) {
			w = m.replacement + w
		}
		if !m.splitWords {
			out = append(out, w)
			continue
		}
		// Cut before every replacement except one at the start of the word.
		for start := 0; start < len(w); {
			_, size := utf8.DecodeRuneInString(w[start:])
			j := strings.Index(w[start+size:], m.replacement)
			if j < 0 {
				out = append(out, w[start:])
				break
			}
			end := start + size + j
			out = append(out, w[start:end])
			start = end
		}
	}
	return out
}
