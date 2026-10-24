// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var ErrInvalidFile = errors.New("piionnx: invalid model file")

const (
	TypePerson       = "person"
	TypeLocation     = "location"
	TypeOrganization = "organization"
)

var entityTypes = map[string]string{
	"PER": TypePerson,
	"LOC": TypeLocation,
	"ORG": TypeOrganization,
}

type tag uint8

const (
	tagO tag = iota
	tagB
	tagI
)

type label struct {
	tag    tag
	entity string
	typ    string
}

func readVocabulary(path string) (*vocabulary, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is operator configuration.
	if err != nil {
		return nil, fmt.Errorf("piionnx: read tokenizer: %w", err)
	}
	var f struct {
		Model struct {
			Type     string         `json:"type"`
			UnkToken string         `json:"unk_token"`
			Vocab    map[string]int `json:"vocab"`
		} `json:"model"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidFile, path, err)
	}
	if f.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("%w: %s: model type %q, want WordPiece", ErrInvalidFile, path, f.Model.Type)
	}
	maxID := -1
	for _, id := range f.Model.Vocab {
		if id < 0 {
			return nil, fmt.Errorf("%w: %s: negative token id %d", ErrInvalidFile, path, id)
		}
		maxID = max(maxID, id)
	}
	unk, ok := f.Model.Vocab[f.Model.UnkToken]
	if !ok {
		return nil, fmt.Errorf("%w: %s: unk token %q not in the vocabulary", ErrInvalidFile, path, f.Model.UnkToken)
	}
	v := &vocabulary{tokens: make([]string, maxID+1), unk: unk}
	for tok, id := range f.Model.Vocab {
		v.tokens[id] = tok
	}
	return v, nil
}

func readLabels(path string) ([]label, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is operator configuration.
	if err != nil {
		return nil, fmt.Errorf("piionnx: read model config: %w", err)
	}
	var f struct {
		ID2Label map[string]string `json:"id2label"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidFile, path, err)
	}
	if len(f.ID2Label) == 0 {
		return nil, fmt.Errorf("%w: %s: no id2label", ErrInvalidFile, path)
	}
	labels := make([]label, len(f.ID2Label))
	seen := make([]bool, len(f.ID2Label))
	for k, name := range f.ID2Label {
		id, err := strconv.Atoi(k)
		if err != nil || id < 0 || id >= len(labels) || seen[id] {
			return nil, fmt.Errorf("%w: %s: id2label key %q is not an id in 0..%d", ErrInvalidFile, path, k, len(labels)-1)
		}
		l, err := parseLabel(name)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalidFile, path, err)
		}
		labels[id], seen[id] = l, true
	}
	return labels, nil
}

func parseLabel(name string) (label, error) {
	if name == "O" {
		return label{tag: tagO}, nil
	}
	prefix, entity, ok := strings.Cut(name, "-")
	if !ok || entity == "" || (prefix != "B" && prefix != "I") {
		return label{}, fmt.Errorf("label %q is not O, B-<TYPE> or I-<TYPE>", name)
	}
	t := tagB
	if prefix == "I" {
		t = tagI
	}
	return label{tag: t, entity: entity, typ: entityTypes[entity]}, nil
}
