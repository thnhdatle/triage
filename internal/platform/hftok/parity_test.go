// SPDX-License-Identifier: Apache-2.0

package hftok_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/platform/hftok"
)

const corpusDir = "../../../testdata/tokenizer"

type corpusLine struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Text     string `json:"text"`
	Lang     string `json:"lang"`
}

type referenceLine struct {
	ID  string `json:"id"`
	IDs []int  `json:"ids"`
}

func tokenizerDir(tb testing.TB) string {
	tb.Helper()
	if dir := os.Getenv("TRIAGE_TOKENIZER_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	require.NoError(tb, err)
	return filepath.Join(home, ".cache", "rayld", "triage", "tokenizers")
}

func loadPinned(tb testing.TB, name string) *hftok.Tokenizer {
	tb.Helper()
	path := filepath.Join(tokenizerDir(tb), name, "tokenizer.json")
	tok, err := hftok.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		require.FailNowf(tb, "tokenizer file missing",
			"%s not found: run `make bootstrap` (or set TRIAGE_TOKENIZER_DIR)", path)
	}
	require.NoError(tb, err)
	return tok
}

func readJSONL[T any](t *testing.T, name string) []T {
	t.Helper()
	f, err := os.Open(filepath.Join(corpusDir, name))
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var v T
		require.NoError(t, json.Unmarshal(sc.Bytes(), &v), "%s: %s", name, sc.Text())
		out = append(out, v)
	}
	require.NoError(t, sc.Err())
	return out
}

type parityCase struct {
	name      string
	tokenizer string
	reference string
}

func TestTokenizer_Encode_MatchesReference(t *testing.T) {
	t.Parallel()
	corpus := readJSONL[corpusLine](t, "corpus.jsonl")
	require.Len(t, corpus, 300)
	byID := make(map[string]corpusLine, len(corpus))
	for _, c := range corpus {
		byID[c.ID] = c
	}
	cases := []parityCase{
		{name: "bert XLM-R Unigram", tokenizer: "bert", reference: "reference_bert.jsonl"},
		{name: "ner WordPiece English", tokenizer: "ner", reference: "reference_ner.jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tok := loadPinned(t, tc.tokenizer)
			refs := readJSONL[referenceLine](t, tc.reference)
			require.NotEmpty(t, refs)
			total, same := map[string]int{}, map[string]int{}
			for _, ref := range refs {
				c, ok := byID[ref.ID]
				require.True(t, ok, "reference id %s not in corpus", ref.ID)
				got, err := tok.Encode(c.Text, true)
				require.NoError(t, err)
				total[c.Category]++
				if assert.Equal(t, ref.IDs, got, "%s %s %q", tc.tokenizer, c.ID, c.Text) {
					same[c.Category]++
				}
				plain, err := tok.Encode(c.Text, false)
				require.NoError(t, err)
				assert.Equal(t, ref.IDs[1:len(ref.IDs)-1], plain, "%s %s without specials", tc.tokenizer, c.ID)
				assert.Equal(t, len(ref.IDs)-2, tok.Count(c.Text), "%s %s count", tc.tokenizer, c.ID)
			}
			cats := make([]string, 0, len(total))
			for k := range total {
				cats = append(cats, k)
			}
			sort.Strings(cats)
			for _, k := range cats {
				t.Logf("%s %-11s %3d/%3d identical", tc.tokenizer, k, same[k], total[k])
			}
		})
	}
}

func TestTokenizer_Encode_Concurrent(t *testing.T) {
	t.Parallel()
	tok := loadPinned(t, "bert")
	corpus := readJSONL[corpusLine](t, "corpus.jsonl")
	want := make([][]int, len(corpus))
	for i, c := range corpus {
		ids, err := tok.Encode(c.Text, true)
		require.NoError(t, err)
		want[i] = ids
	}
	var wg sync.WaitGroup
	mismatch := make(chan string, len(corpus)*4)
	for range 4 {
		wg.Go(func() {
			for i, c := range corpus {
				got, err := tok.Encode(c.Text, true)
				if err != nil || !slices.Equal(got, want[i]) {
					mismatch <- c.ID
				}
			}
		})
	}
	wg.Wait()
	close(mismatch)
	var bad []string
	for id := range mismatch {
		bad = append(bad, id)
	}
	assert.Empty(t, bad)
}
