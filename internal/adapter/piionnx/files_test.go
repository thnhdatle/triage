// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLabels_Dslim(t *testing.T) {
	t.Parallel()
	labels, err := readLabels(writeFile(t, "config.json", dslimLabels))
	require.NoError(t, err)
	require.Len(t, labels, numLabels)
	assert.Equal(t, label{tag: tagO}, labels[lblO])
	assert.Equal(t, label{tag: tagB, entity: "MISC"}, labels[lblBMISC], "MISC has no finding type")
	assert.Equal(t, label{tag: tagB, entity: "PER", typ: TypePerson}, labels[lblBPER])
	assert.Equal(t, label{tag: tagI, entity: "PER", typ: TypePerson}, labels[lblIPER])
	assert.Equal(t, label{tag: tagB, entity: "ORG", typ: TypeOrganization}, labels[lblBORG])
	assert.Equal(t, label{tag: tagI, entity: "LOC", typ: TypeLocation}, labels[lblILOC])
}

func TestReadLabels_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, content, msg string
	}{
		{name: "not json", content: "{", msg: "unexpected end"},
		{name: "no id2label", content: `{"model_type": "bert"}`, msg: "no id2label"},
		{name: "key not a number", content: `{"id2label": {"x": "O"}}`, msg: `key "x"`},
		{name: "key out of range", content: `{"id2label": {"0": "O", "2": "B-PER"}}`, msg: `key "2"`},
		{name: "duplicate id", content: `{"id2label": {"0": "O", "00": "B-PER"}}`, msg: "is not an id"},
		{name: "bad prefix", content: `{"id2label": {"0": "S-PER"}}`, msg: `label "S-PER"`},
		{name: "no entity", content: `{"id2label": {"0": "B-"}}`, msg: `label "B-"`},
		{name: "no dash", content: `{"id2label": {"0": "PER"}}`, msg: `label "PER"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := readLabels(writeFile(t, "config.json", tc.content))
			require.ErrorIs(t, err, ErrInvalidFile)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

func TestReadLabels_Missing(t *testing.T) {
	t.Parallel()
	_, err := readLabels(filepath.Join(t.TempDir(), "config.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestReadVocabulary_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, content, msg string
	}{
		{name: "not json", content: "[", msg: "unexpected end"},
		{name: "not wordpiece", content: `{"model": {"type": "Unigram"}}`, msg: `"Unigram"`},
		{name: "negative id", content: wordPiece(`{"a": -1}`), msg: "negative"},
		{name: "unk absent", content: wordPiece(`{"a": 0}`), msg: "unk token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := readVocabulary(writeFile(t, "tokenizer.json", tc.content))
			require.ErrorIs(t, err, ErrInvalidFile)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

func wordPiece(vocab string) string {
	return `{"model": {"type": "WordPiece", "unk_token": "[UNK]", "vocab": ` + vocab + `}}`
}

func TestReadVocabulary_Missing(t *testing.T) {
	t.Parallel()
	_, err := readVocabulary(filepath.Join(t.TempDir(), "tokenizer.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestReadVocabulary_Pinned(t *testing.T) {
	t.Parallel()
	v, err := readVocabulary(tokenizerPath(t))
	require.NoError(t, err)
	assert.Len(t, v.tokens, 28996)
	assert.Equal(t, "[UNK]", v.tokens[v.unk])
	s, cont, err := v.surface(2142)
	require.NoError(t, err)
	assert.Equal(t, "son", s)
	assert.True(t, cont)
}

// fakeEncoder returns fixed ids per text, or err.
type fakeEncoder struct {
	ids map[string][]int
	err error
}

func (f fakeEncoder) Encode(text string, _ bool) ([]int, error) {
	if f.err != nil && text != "a" {
		return nil, f.err
	}
	return f.ids[text], nil
}

func TestNewAligner_Errors(t *testing.T) {
	t.Parallel()
	v := &vocabulary{tokens: []string{"[UNK]", "a"}}
	_, err := newAligner(fakeEncoder{}, v)
	require.ErrorIs(t, err, ErrInvalidFile, "\"a\" must be one token")
	_, err = newAligner(fakeEncoder{ids: map[string][]int{"a": {1}}, err: errors.New("boom")}, v)
	require.ErrorIs(t, err, ErrInvalidFile)
}

func TestAligner_Probe_EncodeErrorIsWord(t *testing.T) {
	t.Parallel()
	al := &aligner{enc: fakeEncoder{err: errors.New("boom")}}
	assert.Equal(t, classWord, al.probe('x'))
}

func TestAligner_Class_CacheBounded(t *testing.T) {
	t.Parallel()
	_, al := testTokenizer(t)
	al.mu.Lock()
	for r := range rune(maxCachedClasses) {
		al.classes[0x10000+r] = classWord
	}
	al.mu.Unlock()
	assert.Equal(t, classBreak, al.class('，'), "fullwidth comma is punctuation")
	al.mu.RLock()
	defer al.mu.RUnlock()
	assert.Len(t, al.classes, maxCachedClasses)
}
