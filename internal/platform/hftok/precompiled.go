// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"github.com/sugarme/tokenizer/spm"
)

// precompiled is SentencePiece's precompiled_charsmap normalizer (nmt_nfkc
// for XLM-R). The charsmap parsing and double-array layout come from
// sugarme/tokenizer's spm package; the normalization loop is a port of HF
// tokenizers' Precompiled::normalize, replacing sugarme's, which drops
// characters mapped to the empty string (U+200D, U+200B, U+FEFF), rewrites
// non-spacing marks as "U+XXXX" text and repeats the first rune of graphemes
// of six or more bytes.
type precompiled struct {
	trie       spm.Array
	normalized string
}

func newPrecompiled(raw json.RawMessage) (*precompiled, error) {
	var cfg struct {
		Charsmap string `json:"precompiled_charsmap"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%w: precompiled: %w", ErrInvalid, err)
	}
	data, err := base64.StdEncoding.DecodeString(cfg.Charsmap)
	if err != nil {
		return nil, fmt.Errorf("%w: precompiled_charsmap: %w", ErrInvalid, err)
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: precompiled_charsmap too short", ErrInvalid)
	}
	trieSize := int(uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24)
	if trieSize%4 != 0 || trieSize > len(data)-4 || trieSize == 0 {
		return nil, fmt.Errorf("%w: precompiled_charsmap trie size", ErrInvalid)
	}
	normalized, trie := spm.Parse(data)
	return &precompiled{trie: trie, normalized: string(normalized)}, nil
}

// transform returns the replacement of the shortest prefix of chunk present
// in the charsmap (spm_precompiled's transform: results[0] of the common
// prefix search). ok is false when no prefix matches; an empty replacement
// with ok true deletes the chunk.
func (p *precompiled) transform(chunk string) (string, bool) {
	arr := p.trie
	pos := int(arr[0].Offset())
	for i := 0; i < len(chunk); i++ {
		c := chunk[i]
		if c == 0 {
			return "", false
		}
		pos ^= int(c)
		if pos < 0 || pos >= len(arr) {
			return "", false
		}
		unit := arr[pos]
		if unit.Label() != uint(c) {
			return "", false
		}
		pos ^= int(unit.Offset())
		if pos < 0 || pos >= len(arr) {
			return "", false
		}
		if unit.HasLeaf() {
			start := arr[pos].Value()
			if start < 0 || start > len(p.normalized) {
				return "", false
			}
			end := strings.IndexByte(p.normalized[start:], 0)
			if end < 0 {
				return p.normalized[start:], true
			}
			return p.normalized[start : start+end], true
		}
	}
	return "", false
}

func (p *precompiled) normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	state := -1
	for rest := s; rest != ""; {
		var grapheme string
		grapheme, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		if len(grapheme) < 6 {
			if norm, ok := p.transform(grapheme); ok {
				b.WriteString(norm)
				continue
			}
		}
		for i := 0; i < len(grapheme); {
			_, size := utf8.DecodeRuneInString(grapheme[i:])
			part := grapheme[i : i+size]
			if norm, ok := p.transform(part); ok {
				b.WriteString(norm)
			} else {
				b.WriteString(part)
			}
			i += size
		}
	}
	return b.String()
}
