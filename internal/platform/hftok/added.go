// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// addedVocab extracts added tokens from raw text before normalization, as
// HF's AddedVocabulary does for tokens with normalized=false: leftmost-longest
// matching, lstrip/rstrip absorbing adjacent whitespace.
type addedVocab struct {
	tokens []addedToken
	first  [256]bool
}

func newAddedVocab(tokens []addedToken) (*addedVocab, error) {
	v := &addedVocab{}
	for _, t := range tokens {
		switch {
		case t.Content == "":
			return nil, fmt.Errorf("%w: empty added token %d", ErrInvalid, t.ID)
		case t.Normalized:
			return nil, fmt.Errorf("%w: normalized added token %q", ErrUnsupported, t.Content)
		case t.SingleWord:
			return nil, fmt.Errorf("%w: single_word added token %q", ErrUnsupported, t.Content)
		}
		v.tokens = append(v.tokens, t)
		v.first[t.Content[0]] = true
	}
	return v, nil
}

// match returns the longest added token starting at text[i:], or -1.
func (v *addedVocab) match(text string, i int) int {
	best, bestLen := -1, 0
	for k, t := range v.tokens {
		if len(t.Content) > bestLen && strings.HasPrefix(text[i:], t.Content) {
			best, bestLen = k, len(t.Content)
		}
	}
	return best
}

// split calls fn for each piece of text in order: plain segments with id -1
// and added tokens with their id. Empty segments are skipped.
func (v *addedVocab) split(text string, fn func(segment string, id int)) {
	start := 0
	for i := 0; i < len(text); {
		k := -1
		if v.first[text[i]] {
			k = v.match(text, i)
		}
		if k < 0 {
			i++
			continue
		}
		t := v.tokens[k]
		lo, hi := strip(text, start, i, i+len(t.Content), t)
		if start < lo {
			fn(text[start:lo], -1)
		}
		fn(text[lo:hi], t.ID)
		start, i = hi, hi
	}
	if start < len(text) {
		fn(text[start:], -1)
	}
}

// strip widens the match [lo, hi) over adjacent whitespace for lstrip/rstrip
// tokens, never before floor (the end of the previous match).
func strip(text string, floor, lo, hi int, t addedToken) (newLo, newHi int) {
	for t.LStrip && lo > floor {
		r, size := utf8.DecodeLastRuneInString(text[floor:lo])
		if !unicode.IsSpace(r) {
			break
		}
		lo -= size
	}
	for t.RStrip && hi < len(text) {
		r, size := utf8.DecodeRuneInString(text[hi:])
		if !unicode.IsSpace(r) {
			break
		}
		hi += size
	}
	return lo, hi
}
