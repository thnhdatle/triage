// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const corpusPath = "../../../testdata/tokenizer/corpus.jsonl"

// checkSpans asserts the alignment invariants: spans are ordered,
// non-overlapping, on rune boundaries, and each known token's span equals its
// surface once removed runes are dropped.
func checkSpans(t *testing.T, v *aligner, text string, ids []int, spans []span) {
	t.Helper()
	require.Len(t, spans, len(ids))
	bounds := runeBounds(text)
	prev := 0
	for i, sp := range spans {
		require.LessOrEqual(t, prev, sp.start, "token %d starts before the previous end", i)
		require.Less(t, sp.start, sp.end, "token %d is empty", i)
		require.LessOrEqual(t, sp.end, len(text))
		require.True(t, bounds[sp.start] && bounds[sp.end], "token %d not on rune boundaries", i)
		prev = sp.end
		if ids[i] == v.unk {
			continue
		}
		surf, cont, err := v.surface(ids[i])
		require.NoError(t, err)
		assert.Equal(t, cont, sp.cont, "token %d continuation flag", i)
		assert.Equal(t, surf, stripRemoved(v, text[sp.start:sp.end]), "token %d surface", i)
	}
}

// runeBounds marks the offsets where UTF-8 decoding (invalid bytes as one
// U+FFFD each) starts a rune, plus len(s).
func runeBounds(s string) map[int]bool {
	b := map[int]bool{len(s): true}
	for i := 0; i < len(s); {
		b[i] = true
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return b
}

func stripRemoved(al *aligner, s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if al.class(r) != classRemoved {
			b.WriteRune(r)
		}
		i += w
	}
	return b.String()
}

func TestVocabulary_Align_Corpus(t *testing.T) {
	t.Parallel()
	tok, v := testTokenizer(t)
	f, err := os.Open(corpusPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		var c struct{ ID, Text string }
		require.NoError(t, json.Unmarshal(sc.Bytes(), &c))
		ids, err := tok.Encode(c.Text, false)
		require.NoError(t, err)
		spans, err := v.align(c.Text, ids)
		require.NoError(t, err, c.ID)
		checkSpans(t, v, c.Text, ids, spans)
		n++
	}
	require.NoError(t, sc.Err())
	assert.Equal(t, 300, n, "every corpus prompt (all languages) aligns")
}

func TestVocabulary_Align_Cases(t *testing.T) {
	t.Parallel()
	tok, v := testTokenizer(t)
	tests := []struct {
		name  string
		text  string
		want  []string // text[start:end] per token
		cont  []bool
		unks  int
		noErr bool
	}{
		{name: "words and punctuation", text: "John Smith, Berlin.", want: []string{"John", "Smith", ",", "Berlin", "."}},
		{
			name: "subwords", text: "Schwarzenegger", want: []string{"Sc", "hwa", "rz", "ene", "gger"},
			cont: []bool{false, true, true, true, true},
		},
		{name: "unknown cjk then kana", text: "お元気ですか？", want: []string{"お", "元", "気", "ですか", "？"}, unks: 4},
		{name: "leading and inner whitespace", text: " \t John\u3000\nSmith ", want: []string{"John", "Smith"}},
		{name: "zero width inside a word", text: "Jo\u200bhn", want: []string{"Jo\u200bhn"}},
		{name: "invalid utf8 removed", text: "Jo\xffhn", want: []string{"Jo\xffhn"}},
		{name: "literal special tokens", text: "a [CLS] b [UNK] c", want: []string{"a", "[CLS]", "b", "[UNK]", "c"}, unks: 1},
		{name: "unknown word", text: "x \U0001F9EA\U0001F9EA y", want: []string{"x", "\U0001F9EA\U0001F9EA", "y"}, unks: 1},
		{
			name: "unknown word before punctuation", text: "\U0001F9EA\U0001F9EA!",
			want: []string{"\U0001F9EA\U0001F9EA", "!"}, unks: 1,
		},
		{name: "unknown at the end", text: "hi \U0001F9EA", want: []string{"hi", "\U0001F9EA"}, unks: 1},
		{name: "cjk", text: "東京", want: []string{"東", "京"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ids, err := tok.Encode(tc.text, false)
			require.NoError(t, err)
			spans, err := v.align(tc.text, ids)
			require.NoError(t, err)
			checkSpans(t, v, tc.text, ids, spans)
			got := make([]string, len(spans))
			unks := 0
			for i, sp := range spans {
				got[i] = tc.text[sp.start:sp.end]
				if ids[i] == v.unk {
					unks++
				}
				if tc.cont != nil {
					assert.Equal(t, tc.cont[i], sp.cont, "token %d", i)
				}
			}
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.unks, unks)
		})
	}
}

func TestVocabulary_Align_Mismatch(t *testing.T) {
	t.Parallel()
	_, v := testTokenizer(t)
	john := idOf(t, v, "John")
	tests := []struct {
		name string
		text string
		ids  []int
	}{
		{name: "surface absent", text: "Mary", ids: []int{john}},
		{name: "text exhausted", text: "Jo", ids: []int{john}},
		{name: "unknown past the end", text: "John", ids: []int{john, v.unk}},
		{name: "id outside vocabulary", text: "John", ids: []int{len(v.tokens)}},
		{name: "negative id", text: "John", ids: []int{-1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := v.align(tc.text, tc.ids)
			require.ErrorIs(t, err, errAlign)
			assert.NotContains(t, err.Error(), tc.text, "errors never carry text")
		})
	}
}

func TestMatchAt_EmptySurface(t *testing.T) {
	t.Parallel()
	_, al := testTokenizer(t)
	_, _, ok := al.matchAt("abc", 0, "")
	assert.False(t, ok)
}

func idOf(t *testing.T, v *aligner, tok string) int {
	t.Helper()
	for id, s := range v.tokens {
		if s == tok {
			return id
		}
	}
	require.FailNowf(t, "token not in vocabulary", "%q", tok)
	return -1
}

func FuzzVocabulary_Align(f *testing.F) {
	tok, v := testTokenizer(f)
	for _, s := range []string{
		"", "John Smith lives in Berlin.", "Jo\u200bhn", "\xff\xfe", "a [UNK] b", "\U0001F9EA x", "東京 ok",
		"e\u0301te\u0301", "\u0000\u0085\u2028", "##a ## #",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		ids, err := tok.Encode(text, false)
		require.NoError(t, err)
		spans, err := v.align(text, ids)
		require.NoError(t, err, "every hftok encoding aligns")
		checkSpans(t, v, text, ids, spans)
	})
}
