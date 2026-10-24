// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

type wordPiece struct {
	vocab    map[string]int
	prefix   string
	unkID    int
	maxChars int
}

func newWordPiece(raw json.RawMessage) (*wordPiece, error) {
	var cfg struct {
		Vocab                   map[string]int `json:"vocab"`
		UnkToken                string         `json:"unk_token"`
		ContinuingSubwordPrefix string         `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    int            `json:"max_input_chars_per_word"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%w: WordPiece: %w", ErrInvalid, err)
	}
	unk, ok := cfg.Vocab[cfg.UnkToken]
	if !ok {
		return nil, fmt.Errorf("%w: WordPiece unk_token %q not in vocab", ErrInvalid, cfg.UnkToken)
	}
	return &wordPiece{
		vocab:    cfg.Vocab,
		prefix:   cfg.ContinuingSubwordPrefix,
		unkID:    unk,
		maxChars: cfg.MaxInputCharsPerWord,
	}, nil
}

func (w *wordPiece) tokenize(word string, dst []int) []int {
	if utf8.RuneCountInString(word) > w.maxChars {
		return append(dst, w.unkID)
	}
	first := len(dst)
	for start := 0; start < len(word); {
		end := len(word)
		found := -1
		for start < end {
			sub := word[start:end]
			if start > 0 {
				sub = w.prefix + sub
			}
			if id, ok := w.vocab[sub]; ok {
				found = id
				break
			}
			_, size := utf8.DecodeLastRuneInString(word[start:end])
			end -= size
		}
		if found < 0 {
			return append(dst[:first], w.unkID)
		}
		dst = append(dst, found)
		start = end
	}
	return dst
}
