// SPDX-License-Identifier: Apache-2.0

package hftok_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/platform/hftok"
)

func writeTokenizer(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokenizer.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func mustLoad(t *testing.T, body string) *hftok.Tokenizer {
	t.Helper()
	tok, err := hftok.Load(writeTokenizer(t, body))
	require.NoError(t, err)
	return tok
}

const template = `{"type":"TemplateProcessing",
 "single":[{"SpecialToken":{"id":"<s>","type_id":0}},{"Sequence":{"id":"A","type_id":0}},
           {"SpecialToken":{"id":"</s>","type_id":0}}],
 "special_tokens":{"<s>":{"id":"<s>","ids":[0],"tokens":["<s>"]},"</s>":{"id":"</s>","ids":[2],"tokens":["</s>"]}}}`

const unigramVocab = `[["<s>",0],["<pad>",0],["</s>",0],["<unk>",0],["▁",-2],["▁a",-1],["b",-3],
 ["▁ab",-2.5],["▁c",-1],["c",-4],["<mask>",0]]`

func unigramTokenizer(pre string) string {
	return `{"added_tokens":[
  {"id":0,"content":"<s>","special":true},{"id":2,"content":"</s>","special":true},
  {"id":10,"content":"<mask>","special":true,"lstrip":true},
  {"id":3,"content":"<unk>","special":true,"rstrip":true}],
 "normalizer":null,"pre_tokenizer":` + pre + `,"post_processor":` + template + `,
 "model":{"type":"Unigram","unk_id":3,"vocab":` + unigramVocab + `}}`
}

const metaspaceAlways = `{"type":"Sequence","pretokenizers":[{"type":"WhitespaceSplit"},
 {"type":"Metaspace","replacement":"▁","prepend_scheme":"always","split":true}]}`

const wordPieceTokenizer = `{
 "added_tokens":[{"id":1,"content":"[CLS]","special":true},{"id":2,"content":"[SEP]","special":true}],
 "normalizer":{"type":"Sequence","normalizers":[{"type":"BertNormalizer","clean_text":true,
   "handle_chinese_chars":true,"strip_accents":null,"lowercase":false}]},
 "pre_tokenizer":{"type":"BertPreTokenizer"},
 "post_processor":{"type":"TemplateProcessing","single":[{"SpecialToken":{"id":"[CLS]"}},{"Sequence":{"id":"A"}},
   {"SpecialToken":{"id":"[SEP]"}}],"special_tokens":{"[CLS]":{"ids":[1]},"[SEP]":{"ids":[2]}}},
 "model":{"type":"WordPiece","unk_token":"[UNK]","continuing_subword_prefix":"##","max_input_chars_per_word":5,
   "vocab":{"[UNK]":0,"[CLS]":1,"[SEP]":2,"un":3,"##aff":4,"##able":5,",":6,"中":7,"a":8}}}`

func TestTruncate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ids  []int
		max  int
		want []int
	}{
		{name: "shorter than max", ids: []int{0, 5, 2}, max: 512, want: []int{0, 5, 2}},
		{name: "exactly max", ids: []int{0, 5, 6, 2}, max: 4, want: []int{0, 5, 6, 2}},
		{name: "keeps final special", ids: []int{0, 5, 6, 7, 8, 2}, max: 4, want: []int{0, 5, 6, 2}},
		{name: "max one", ids: []int{0, 5, 2}, max: 1, want: []int{2}},
		{name: "max zero", ids: []int{0, 5, 2}, max: 0, want: []int{}},
		{name: "empty input", ids: nil, max: 3, want: []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := append([]int(nil), tc.ids...)
			got := hftok.Truncate(in, tc.max)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ids, in, "input must not be modified")
			if len(got) > 0 && len(in) > 0 {
				got[0] = -1
				assert.NotEqual(t, -1, in[0], "result must not alias input")
			}
		})
	}
}

func TestTruncate_MatchesHFOnLongPrompt(t *testing.T) {
	t.Parallel()
	tok := loadPinned(t, "bert")
	ids, err := tok.Encode(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 200), true)
	require.NoError(t, err)
	require.Greater(t, len(ids), 512)
	got := hftok.Truncate(ids, 512)
	require.Len(t, got, 512)
	assert.Equal(t, 0, got[0])
	assert.Equal(t, 2, got[511])
	assert.Equal(t, ids[:511], got[:511])
}

func TestLoad_Errors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want error
	}{
		{name: "invalid json", body: `{`, want: hftok.ErrInvalid},
		{name: "no model", body: `{"model":null}`, want: hftok.ErrInvalid},
		{name: "unsupported model", body: `{"model":{"type":"BPE"}}`, want: hftok.ErrUnsupported},
		{
			name: "unsupported normalizer", body: `{"normalizer":{"type":"NFKC"},"model":{"type":"BPE"}}`,
			want: hftok.ErrUnsupported,
		},
		{
			name: "unsupported pre-tokenizer", body: `{"pre_tokenizer":{"type":"ByteLevel"},"model":{"type":"BPE"}}`,
			want: hftok.ErrUnsupported,
		},
		{name: "metaspace first", body: `{"pre_tokenizer":{"type":"Metaspace","replacement":"▁",` +
			`"prepend_scheme":"first"},"model":{"type":"BPE"}}`, want: hftok.ErrUnsupported},
		{name: "lowercase bert", body: `{"normalizer":{"type":"BertNormalizer","lowercase":true},` +
			`"model":{"type":"BPE"}}`, want: hftok.ErrUnsupported},
		{name: "normalized added token", body: `{"added_tokens":[{"id":1,"content":"x","normalized":true}],` +
			`"model":{"type":"BPE"}}`, want: hftok.ErrUnsupported},
		{
			name: "unigram without unk", body: `{"model":{"type":"Unigram","vocab":[["a",0]]}}`,
			want: hftok.ErrUnsupported,
		},
		{name: "unigram byte fallback", body: `{"model":{"type":"Unigram","unk_id":0,"byte_fallback":true,` +
			`"vocab":[["a",0]]}}`, want: hftok.ErrUnsupported},
		{
			name: "wordpiece unk missing", body: `{"model":{"type":"WordPiece","unk_token":"[UNK]","vocab":{}}}`,
			want: hftok.ErrInvalid,
		},
		{name: "bad charsmap", body: `{"normalizer":{"type":"Precompiled","precompiled_charsmap":"AAAA"},` +
			`"model":{"type":"BPE"}}`, want: hftok.ErrInvalid},
		{name: "unsupported post-processor", body: `{"post_processor":{"type":"RobertaProcessing"},` +
			`"model":{"type":"Unigram","unk_id":0,"vocab":[["a",0]]}}`, want: hftok.ErrUnsupported},
		{name: "template undefined token", body: `{"post_processor":{"type":"TemplateProcessing",` +
			`"single":[{"SpecialToken":{"id":"<x>"}},{"Sequence":{"id":"A"}}],"special_tokens":{}},` +
			`"model":{"type":"Unigram","unk_id":0,"vocab":[["a",0]]}}`, want: hftok.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := hftok.Load(writeTokenizer(t, tc.body))
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := hftok.Load(filepath.Join(t.TempDir(), "absent.json"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	assert.NotErrorIs(t, err, hftok.ErrInvalid)
}

func TestEncode_Unigram(t *testing.T) {
	t.Parallel()
	tok := mustLoad(t, unigramTokenizer(metaspaceAlways))
	cases := []struct {
		name    string
		text    string
		special bool
		want    []int
	}{
		{name: "empty", text: "", special: true, want: []int{0, 2}},
		{name: "best path prefers higher total score", text: "ab", want: []int{7}},
		{name: "words each get the metaspace", text: "a  c", want: []int{5, 8}},
		{name: "unknown run fused into one unk", text: "axyzb", want: []int{5, 3, 6}},
		{name: "special token in text", text: "a</s>c", special: true, want: []int{0, 5, 2, 8, 2}},
		{name: "lstrip absorbs left whitespace", text: "a  <mask> c", want: []int{5, 10, 8}},
		{name: "rstrip absorbs right whitespace", text: "a<unk>  c", want: []int{5, 3, 8}},
		{name: "leading metaspace not doubled", text: "▁a▁▁c", want: []int{5, 4, 8}},
		{name: "invalid utf8 becomes replacement", text: "a\xff", want: []int{5, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tok.Encode(tc.text, tc.special)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			if !tc.special {
				assert.Equal(t, len(tc.want), tok.Count(tc.text))
			}
		})
	}
}

func TestEncode_MetaspaceNoSplit(t *testing.T) {
	t.Parallel()
	tok := mustLoad(t, unigramTokenizer(`{"type":"Metaspace","replacement":"▁","add_prefix_space":false,`+
		`"split":false}`))
	got, err := tok.Encode("a c", false)
	require.NoError(t, err)
	// "a▁c": no prefix, not split; "a" is unknown, "▁c" is a piece.
	assert.Equal(t, []int{3, 8}, got)
}

func TestEncode_WordPiece(t *testing.T) {
	t.Parallel()
	tok := mustLoad(t, wordPieceTokenizer)
	cases := []struct {
		name string
		text string
		want []int
	}{
		{name: "subwords", text: "unaff", want: []int{1, 3, 4, 2}},
		{name: "punctuation isolated", text: "un,a", want: []int{1, 3, 6, 8, 2}},
		{name: "unmatched remainder is unk", text: "unx", want: []int{1, 0, 2}},
		{name: "too long word is unk", text: "unaffable", want: []int{1, 0, 2}},
		{name: "chinese chars split", text: "a中a", want: []int{1, 8, 7, 8, 2}},
		{name: "control removed whitespace kept", text: "un\u0007\taff", want: []int{1, 3, 0, 2}},
		{name: "literal special", text: "a[SEP]a", want: []int{1, 8, 2, 8, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tok.Encode(tc.text, true)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
