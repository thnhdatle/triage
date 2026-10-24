// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

var ErrUnsupported = errors.New("hftok: unsupported tokenizer component")

var ErrInvalid = errors.New("hftok: invalid tokenizer file")

type normalizer interface {
	normalize(s string) string
}

type preTokenizer interface {
	split(words []string) []string
}

type model interface {
	tokenize(word string, dst []int) []int
}

// Tokenizer encodes text with the pipeline described by a tokenizer.json.
type Tokenizer struct {
	added  *addedVocab
	norm   normalizer
	pre    preTokenizer
	model  model
	prefix []int
	suffix []int
}

// Load reads and validates the tokenizer.json at path.
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

// Encode returns the token ids of text.
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

func (t *Tokenizer) Count(text string) int {
	ids, err := t.Encode(text, false)
	if err != nil {
		return 0
	}
	return len(ids)
}

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
