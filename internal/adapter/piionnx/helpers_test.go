// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/platform/hftok"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const dslimLabels = `{"id2label": {"0": "O", "1": "B-MISC", "2": "I-MISC", "3": "B-PER", "4": "I-PER",
  "5": "B-ORG", "6": "I-ORG", "7": "B-LOC", "8": "I-LOC"}, "model_type": "bert"}`

const (
	lblO = iota
	lblBMISC
	lblIMISC
	lblBPER
	lblIPER
	lblBORG
	lblIORG
	lblBLOC
	lblILOC
	numLabels
)

func tokenizerPath(tb testing.TB) string {
	tb.Helper()
	dir := os.Getenv("TRIAGE_TOKENIZER_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		require.NoError(tb, err)
		dir = filepath.Join(home, ".cache", "rayld", "triage", "tokenizers")
	}
	path := filepath.Join(dir, "ner", "tokenizer.json")
	if _, err := os.Stat(path); err != nil {
		require.FailNowf(tb, "NER tokenizer missing",
			"%s not found (%v): run `make bootstrap` (or set TRIAGE_TOKENIZER_DIR)", path, err)
	}
	return path
}

func testTokenizer(tb testing.TB) (*hftok.Tokenizer, *aligner) {
	tb.Helper()
	path := tokenizerPath(tb)
	tok, err := hftok.Load(path)
	require.NoError(tb, err)
	v, err := readVocabulary(path)
	require.NoError(tb, err)
	al, err := newAligner(tok, v)
	require.NoError(tb, err)
	return tok, al
}

func writeFile(tb testing.TB, name, content string) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), name)
	require.NoError(tb, os.WriteFile(path, []byte(content), 0o600))
	return path
}
