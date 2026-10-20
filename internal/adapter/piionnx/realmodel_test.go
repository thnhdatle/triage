// SPDX-License-Identifier: Apache-2.0

//go:build realmodel && cgo && !noonnx

package piionnx

// This test runs the real dslim/bert-base-NER export. It is opt-in:
//
//	triage models pull ner   # or make bootstrap + models pull
//	go test -race -count=1 -tags realmodel -run RealModel ./internal/adapter/piionnx/
//
// The model is read from TRIAGE_NER_MODEL_DIR (the directory holding
// model.onnx, tokenizer.json and config.json) or from the pinned cache
// location ~/.cache/rayld/triage/models/ner/<revision>/onnx. A missing file
// fails the test with instructions; it is never skipped.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// nerRevision is the pinned revision in models/manifest.yaml.
const nerRevision = "d1a3e8f13f8c3566299d95fcfc9a8d2382a9affc"

func realFiles(t *testing.T) Files {
	t.Helper()
	dir := os.Getenv("TRIAGE_NER_MODEL_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		dir = filepath.Join(home, ".cache", "rayld", "triage", "models", "ner", nerRevision, "onnx")
	}
	files := Files{
		Model:     filepath.Join(dir, "model.onnx"),
		Tokenizer: filepath.Join(dir, "tokenizer.json"),
		Config:    filepath.Join(dir, "config.json"),
	}
	for _, p := range []string{files.Model, files.Tokenizer, files.Config} {
		if _, err := os.Stat(p); err != nil {
			require.FailNowf(t, "real NER model missing",
				"%s: %v: run `triage models pull ner` or set TRIAGE_NER_MODEL_DIR", p, err)
		}
	}
	return files
}

func TestDetector_RealModel_EnglishEntities(t *testing.T) {
	t.Parallel()
	d, err := New(newRuntime(t), realFiles(t), WithTimeout(30*time.Second))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, d.Close(context.Background())) })
	require.NoError(t, d.Probe(context.Background()))

	cases := []struct {
		text string
		want []want
	}{
		{"Barack Obama was born in Honolulu, Hawaii.", []want{
			{TypePerson, "Barack Obama", 0}, {TypeLocation, "Honolulu", 0}, {TypeLocation, "Hawaii", 0},
		}},
		{"Summarize the email from Maria Garcia at Microsoft.", []want{
			{TypePerson, "Maria Garcia", 0}, {TypeOrganization, "Microsoft", 0},
		}},
		{"Angela Merkel met Emmanuel Macron in Berlin.", []want{
			{TypePerson, "Angela Merkel", 0}, {TypePerson, "Emmanuel Macron", 0}, {TypeLocation, "Berlin", 0},
		}},
		{"I'm flying from New York to London with John Smith.", []want{
			{TypeLocation, "New York", 0}, {TypeLocation, "London", 0}, {TypePerson, "John Smith", 0},
		}},
	}
	expected, found, reported := 0, 0, 0
	for _, c := range cases {
		got, err := d.Detect(context.Background(), c.text)
		require.NoError(t, err)
		reported += len(got)
		for _, f := range got {
			require.Equal(t, f.Value, c.text[f.Start:f.End])
			require.GreaterOrEqual(t, f.Confidence, DefaultMinScore)
		}
		for _, w := range c.want {
			expected++
			for _, f := range got {
				if f.Type == w.typ && f.Value == w.value {
					found++
					break
				}
			}
		}
	}
	t.Logf("recall %d/%d, precision %d/%d", found, expected, found, reported)
	assert.GreaterOrEqual(t, float64(found)/float64(expected), 0.95)
}

func TestDetector_RealModel_LongTextWindows(t *testing.T) {
	t.Parallel()
	d, err := New(newRuntime(t), realFiles(t), WithTimeout(60*time.Second), WithMaxTokens(4096))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, d.Close(context.Background())) })
	text := strings.Repeat("The weather was mild and the meeting ran long. ", 120) +
		"Afterwards Angela Merkel flew to Paris."
	got, err := d.Detect(context.Background(), text)
	require.NoError(t, err)
	values := make([]string, 0, len(got))
	for _, f := range got {
		require.NotEqual(t, privacy.TypeNERUnavailable, f.Type)
		values = append(values, f.Value)
	}
	assert.Contains(t, values, "Angela Merkel", "entity in the last window is found")
	assert.Contains(t, values, "Paris")
}
