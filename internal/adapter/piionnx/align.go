// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// errAlign reports a token whose surface form could not be found in the text.
// It never carries text: only token positions.
var errAlign = errors.New("piionnx: token offsets could not be aligned")

// unkLiteral is the unknown token written literally in a text; hftok matches
// it as an added token, so it has the unk id but a verbatim surface.
const unkLiteral = "[UNK]"

// contPrefix marks a WordPiece continuation token.
const contPrefix = "##"

// maxCachedClasses bounds the per-detector cache of non-ASCII rune classes.
const maxCachedClasses = 1 << 16

// span is the byte range of one token in the original text. cont marks a
// WordPiece continuation ("##…"), which belongs to the previous token's word.
type span struct {
	start, end int
	cont       bool
}

// vocabulary maps token ids back to WordPiece strings.
type vocabulary struct {
	tokens []string // id -> token; "" for ids without a token
	unk    int
}

// surface returns the text a token covers and whether it continues a word.
func (v *vocabulary) surface(id int) (string, bool, error) {
	if id < 0 || id >= len(v.tokens) || v.tokens[id] == "" {
		return "", false, fmt.Errorf("%w: id %d outside the vocabulary", errAlign, id)
	}
	tok := v.tokens[id]
	if len(tok) > len(contPrefix) && strings.HasPrefix(tok, contPrefix) {
		return tok[len(contPrefix):], true, nil
	}
	return tok, false, nil
}

// charClass is how the tokenizer's normalizer and pre-tokenizer treat a rune.
type charClass uint8

const (
	classWord    charClass = iota // part of a word
	classSpace                    // separates words, dropped
	classRemoved                  // deleted by clean_text (controls, format characters, U+FFFD)
	classBreak                    // a word on its own (punctuation, CJK ideograph)
)

// encoder is the part of hftok.Tokenizer the aligner uses.
type encoder interface {
	Encode(text string, addSpecial bool) ([]int, error)
}

// aligner maps token ids back to byte offsets. Rune classes are not
// re-implemented: they are read from the tokenizer itself by encoding
// "a"+r+"a", so they are exactly HF's (frozen Unicode tables).
// Safe for concurrent use; mu guards classes.
type aligner struct {
	*vocabulary
	enc     encoder
	a       int   // id of "a"
	aa      []int // ids of "aa"
	ascii   [utf8.RuneSelf]charClass
	mu      sync.RWMutex
	classes map[rune]charClass
}

func newAligner(enc encoder, v *vocabulary) (*aligner, error) {
	al := &aligner{vocabulary: v, enc: enc, classes: map[rune]charClass{}}
	a, err := enc.Encode("a", false)
	if err != nil || len(a) != 1 {
		return nil, fmt.Errorf("%w: tokenizer does not encode %q as one token", ErrInvalidFile, "a")
	}
	al.a = a[0]
	if al.aa, err = enc.Encode("aa", false); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFile, err)
	}
	for r := range rune(utf8.RuneSelf) {
		al.ascii[r] = al.probe(r)
	}
	return al, nil
}

// probe classifies r by how the tokenizer encodes "a"+r+"a".
func (al *aligner) probe(r rune) charClass {
	ids, err := al.enc.Encode("a"+string(r)+"a", false)
	switch {
	case err != nil:
		return classWord
	case slices.Equal(ids, []int{al.a, al.a}):
		return classSpace
	case slices.Equal(ids, al.aa):
		return classRemoved
	case len(ids) == 3 && ids[0] == al.a && ids[2] == al.a:
		return classBreak
	}
	return classWord
}

// class returns the class of the rune decoded as r with width w; invalid
// UTF-8 becomes U+FFFD in hftok and is removed.
func (al *aligner) class(r rune) charClass {
	if r == utf8.RuneError {
		return classRemoved
	}
	if r < utf8.RuneSelf {
		return al.ascii[r]
	}
	al.mu.RLock()
	c, ok := al.classes[r]
	al.mu.RUnlock()
	if ok {
		return c
	}
	c = al.probe(r)
	al.mu.Lock()
	if len(al.classes) < maxCachedClasses {
		al.classes[r] = c
	}
	al.mu.Unlock()
	return c
}

// align maps the ids hftok produced for text (without special tokens) to byte
// spans of text. The NER tokenizer is cased BERT (BertNormalizer without
// lowercasing or accent stripping), so every token except [UNK] occurs
// verbatim in text once removed runes are skipped, and an [UNK] covers one
// whole word. Spans are on rune boundaries, ordered and non-overlapping. Any
// mismatch fails with errAlign, which callers treat as "could not scan"
// (fail-closed).
func (al *aligner) align(text string, ids []int) ([]span, error) {
	spans := make([]span, len(ids))
	pos := 0
	for i, id := range ids {
		var sp span
		var err error
		if id == al.unk {
			sp, err = al.alignUnknown(text, pos)
		} else {
			sp, err = al.alignKnown(text, pos, id)
		}
		if err != nil {
			return nil, fmt.Errorf("%w (token %d of %d)", err, i+1, len(ids))
		}
		spans[i] = sp
		pos = sp.end
	}
	return spans, nil
}

func (al *aligner) alignKnown(text string, pos, id int) (span, error) {
	s, cont, err := al.surface(id)
	if err != nil {
		return span{}, err
	}
	if !cont {
		pos = al.skipBetweenWords(text, pos)
	}
	start, end, ok := al.matchAt(text, pos, s)
	if !ok {
		return span{}, errAlign
	}
	return span{start: start, end: end, cont: cont}, nil
}

// alignUnknown spans an [UNK]: the literal "[UNK]" or a whole word that
// WordPiece could not split. The word ends before the next space or break
// rune; a break rune is a word on its own.
func (al *aligner) alignUnknown(text string, pos int) (span, error) {
	pos = al.skipBetweenWords(text, pos)
	if pos >= len(text) {
		return span{}, errAlign
	}
	if strings.HasPrefix(text[pos:], unkLiteral) {
		return span{start: pos, end: pos + len(unkLiteral)}, nil
	}
	first, w := utf8.DecodeRuneInString(text[pos:])
	end := pos + w
	if al.class(first) == classBreak {
		return span{start: pos, end: end}, nil
	}
	for i := end; i < len(text); {
		r, w := utf8.DecodeRuneInString(text[i:])
		c := al.class(r)
		if c == classSpace || c == classBreak {
			break
		}
		i += w
		if c == classWord {
			end = i
		}
	}
	return span{start: pos, end: end}, nil
}

// matchAt matches s against text from pos, skipping removed runes before and
// inside the match. It returns the byte range from the first to the last
// matched rune.
func (al *aligner) matchAt(text string, pos int, s string) (start, end int, ok bool) {
	start = -1
	i := pos
	for _, want := range s {
		for {
			if i >= len(text) {
				return 0, 0, false
			}
			r, w := utf8.DecodeRuneInString(text[i:])
			if r == want && r != utf8.RuneError {
				if start < 0 {
					start = i
				}
				i += w
				break
			}
			if al.class(r) != classRemoved {
				return 0, 0, false
			}
			i += w
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, i, true
}

// skipBetweenWords advances over space and removed runes.
func (al *aligner) skipBetweenWords(text string, pos int) int {
	for pos < len(text) {
		r, w := utf8.DecodeRuneInString(text[pos:])
		if c := al.class(r); c != classSpace && c != classRemoved {
			break
		}
		pos += w
	}
	return pos
}
