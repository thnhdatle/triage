// SPDX-License-Identifier: Apache-2.0

package hftok

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// fileConfig is the subset of tokenizer.json that drives encoding.
type fileConfig struct {
	AddedTokens   []addedToken    `json:"added_tokens"`
	Normalizer    json.RawMessage `json:"normalizer"`
	PreTokenizer  json.RawMessage `json:"pre_tokenizer"`
	PostProcessor json.RawMessage `json:"post_processor"`
	Model         json.RawMessage `json:"model"`
}

type addedToken struct {
	Content    string `json:"content"`
	ID         int    `json:"id"`
	SingleWord bool   `json:"single_word"`
	LStrip     bool   `json:"lstrip"`
	RStrip     bool   `json:"rstrip"`
	Normalized bool   `json:"normalized"`
	Special    bool   `json:"special"`
}

type component struct {
	Type string `json:"type"`
}

func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func componentType(raw json.RawMessage) (string, error) {
	var c component
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return c.Type, nil
}

func parse(data []byte) (*Tokenizer, error) {
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	tok := &Tokenizer{}
	var err error
	if tok.added, err = newAddedVocab(cfg.AddedTokens); err != nil {
		return nil, err
	}
	if tok.norm, err = parseNormalizer(cfg.Normalizer); err != nil {
		return nil, err
	}
	if tok.pre, err = parsePreTokenizer(cfg.PreTokenizer); err != nil {
		return nil, err
	}
	if tok.model, err = parseModel(cfg.Model); err != nil {
		return nil, err
	}
	if tok.prefix, tok.suffix, err = parsePostProcessor(cfg.PostProcessor); err != nil {
		return nil, err
	}
	return tok, nil
}

func parseNormalizer(raw json.RawMessage) (normalizer, error) {
	if isNull(raw) {
		return nil, nil //nolint:nilnil // a tokenizer without normalizer is valid.
	}
	typ, err := componentType(raw)
	if err != nil {
		return nil, err
	}
	switch typ {
	case "Precompiled":
		return newPrecompiled(raw)
	case "BertNormalizer":
		return newBertNormalizer(raw)
	case "Sequence":
		var seq struct {
			Normalizers []json.RawMessage `json:"normalizers"`
		}
		if err := json.Unmarshal(raw, &seq); err != nil {
			return nil, fmt.Errorf("%w: normalizer sequence: %w", ErrInvalid, err)
		}
		out := make(normalizerSeq, 0, len(seq.Normalizers))
		for _, r := range seq.Normalizers {
			n, err := parseNormalizer(r)
			if err != nil {
				return nil, err
			}
			if n != nil {
				out = append(out, n)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: normalizer %q", ErrUnsupported, typ)
	}
}

type normalizerSeq []normalizer

func (s normalizerSeq) normalize(text string) string {
	for _, n := range s {
		text = n.normalize(text)
	}
	return text
}

func parsePreTokenizer(raw json.RawMessage) (preTokenizer, error) {
	if isNull(raw) {
		return nil, nil //nolint:nilnil // a tokenizer without pre-tokenizer is valid.
	}
	typ, err := componentType(raw)
	if err != nil {
		return nil, err
	}
	switch typ {
	case "WhitespaceSplit":
		return whitespaceSplit{}, nil
	case "BertPreTokenizer":
		return bertPreTokenizer{}, nil
	case "Metaspace":
		return newMetaspace(raw)
	case "Sequence":
		var seq struct {
			PreTokenizers []json.RawMessage `json:"pretokenizers"`
		}
		if err := json.Unmarshal(raw, &seq); err != nil {
			return nil, fmt.Errorf("%w: pre-tokenizer sequence: %w", ErrInvalid, err)
		}
		out := make(preTokenizerSeq, 0, len(seq.PreTokenizers))
		for _, r := range seq.PreTokenizers {
			p, err := parsePreTokenizer(r)
			if err != nil {
				return nil, err
			}
			if p != nil {
				out = append(out, p)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: pre-tokenizer %q", ErrUnsupported, typ)
	}
}

type preTokenizerSeq []preTokenizer

func (s preTokenizerSeq) split(words []string) []string {
	for _, p := range s {
		words = p.split(words)
	}
	return words
}

func parseModel(raw json.RawMessage) (model, error) {
	if isNull(raw) {
		return nil, fmt.Errorf("%w: no model", ErrInvalid)
	}
	typ, err := componentType(raw)
	if err != nil {
		return nil, err
	}
	switch typ {
	case "Unigram":
		return newUnigram(raw)
	case "WordPiece":
		return newWordPiece(raw)
	default:
		return nil, fmt.Errorf("%w: model %q", ErrUnsupported, typ)
	}
}

// templatePiece is one element of TemplateProcessing.single.
type templatePiece struct {
	SpecialToken *struct {
		ID string `json:"id"`
	} `json:"SpecialToken"`
	Sequence *struct {
		ID string `json:"id"`
	} `json:"Sequence"`
}

func parsePostProcessor(raw json.RawMessage) (prefix, suffix []int, err error) {
	if isNull(raw) {
		return nil, nil, nil
	}
	typ, err := componentType(raw)
	if err != nil {
		return nil, nil, err
	}
	if typ != "TemplateProcessing" {
		return nil, nil, fmt.Errorf("%w: post-processor %q", ErrUnsupported, typ)
	}
	var tp struct {
		Single        []templatePiece `json:"single"`
		SpecialTokens map[string]struct {
			IDs []int `json:"ids"`
		} `json:"special_tokens"`
	}
	if err := json.Unmarshal(raw, &tp); err != nil {
		return nil, nil, fmt.Errorf("%w: post-processor: %w", ErrInvalid, err)
	}
	seen := false
	for _, p := range tp.Single {
		switch {
		case p.Sequence != nil:
			if seen {
				return nil, nil, fmt.Errorf("%w: template with two sequences", ErrInvalid)
			}
			seen = true
		case p.SpecialToken != nil:
			st, ok := tp.SpecialTokens[p.SpecialToken.ID]
			if !ok {
				return nil, nil, fmt.Errorf("%w: template token %q undefined", ErrInvalid, p.SpecialToken.ID)
			}
			if seen {
				suffix = append(suffix, st.IDs...)
			} else {
				prefix = append(prefix, st.IDs...)
			}
		default:
			return nil, nil, fmt.Errorf("%w: template piece", ErrInvalid)
		}
	}
	if !seen {
		return nil, nil, fmt.Errorf("%w: template without sequence", ErrInvalid)
	}
	return prefix, suffix, nil
}
