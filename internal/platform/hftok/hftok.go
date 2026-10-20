// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrUnsupported reports a tokenizer.json component (or option) that hftok
// does not implement; such files are rejected instead of encoded inexactly.
var ErrUnsupported = errors.New("hftok: unsupported tokenizer component")

// ErrInvalid reports a malformed tokenizer.json.
var ErrInvalid = errors.New("hftok: invalid tokenizer file")

// normalizer rewrites one text segment (between added tokens).
type normalizer interface {
	normalize(s string) string
}

// preTokenizer splits normalized text into words; it appends to dst.
type preTokenizer interface {
	split(words []string) []string
}

// model turns one word into ids; it appends to dst.
type model interface {
	tokenize(word string, dst []int) []int
}

// Tokenizer encodes text with the pipeline described by a tokenizer.json:
// added-token extraction, normalizer, pre-tokenizer, model and the single
// sequence template of the post-processor. It is immutable after [Load] and
// safe for concurrent use. Truncation and padding settings in the file are
// ignored; callers truncate explicitly with [Truncate].
type Tokenizer struct {
	added  *addedVocab
	norm   normalizer
	pre    preTokenizer
	model  model
	prefix []int
	suffix []int
}

// Load reads and validates the tokenizer.json at path. A missing file yields
// an error matching [os.ErrNotExist]; unknown components yield
// [ErrUnsupported].
func Load(path string) (*Tokenizer, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is operator configuration.
	if err != nil {
		return nil, fmt.Errorf("hftok: read tokenizer: %w", err)
	}
	tok, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("hftok: load %s: %w", path, err)
	}
	return tok, nil
}

// Encode returns the token ids of text. With addSpecial the post-processor's
// special tokens (`<s>`…`</s>` for XLM-R, `[CLS]`…`[SEP]` for BERT) surround
// the sequence. Special tokens written literally in text are recognised as in
// HF tokenizers. Invalid UTF-8 is replaced by U+FFFD first. The error is
// reserved for future components; the supported ones cannot fail once loaded.
func (t *Tokenizer) Encode(text string, addSpecial bool) ([]int, error) {
	text = strings.ToValidUTF8(text, "�")
	ids := make([]int, 0, len(text)/2+len(t.prefix)+len(t.suffix))
	if addSpecial {
		ids = append(ids, t.prefix...)
	}
	var words []string
	t.added.split(text, func(segment string, id int) {
		if id >= 0 {
			ids = append(ids, id)
			return
		}
		if t.norm != nil {
			segment = t.norm.normalize(segment)
		}
		words = append(words[:0], segment)
		if t.pre != nil {
			words = t.pre.split(words)
		}
		for _, w := range words {
			if w != "" {
				ids = t.model.tokenize(w, ids)
			}
		}
	})
	if addSpecial {
		ids = append(ids, t.suffix...)
	}
	return ids, nil
}

// Count returns the number of tokens of text without special tokens, the
// exact prompt token count used by selection and length rules.
func (t *Tokenizer) Count(text string) int {
	ids, err := t.Encode(text, false)
	if err != nil {
		return 0
	}
	return len(ids)
}

// Truncate right-truncates ids, as produced by Encode with special tokens, to
// at most maxLen ids while keeping the final id (`</s>` or `[SEP]`) last; this
// equals HF truncation (direction right, max_length maxLen) applied before the
// template. The result never aliases ids. maxLen < 1 yields an empty slice.
func Truncate(ids []int, maxLen int) []int {
	if maxLen < 1 || len(ids) == 0 {
		return []int{}
	}
	if len(ids) <= maxLen {
		return append([]int(nil), ids...)
	}
	out := make([]int, 0, maxLen)
	out = append(out, ids[:maxLen-1]...)
	return append(out, ids[len(ids)-1])
}
