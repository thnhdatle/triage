// SPDX-License-Identifier: Apache-2.0

package prompts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/bench/prompts"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gsm8k", "test.jsonl"),
		`{"question": "q0", "answer": "a"}`+"\n\n"+`{"question": "q1 long", "answer": "b"}`+"\n")
	writeFile(t, filepath.Join(dir, "mmlu", "test", "law.jsonl"),
		`{"subject": "law", "question": "Is it?", "choices": ["yes", "no"], "answer": 0}`+"\n")
	writeFile(t, filepath.Join(dir, "mmlu", "test", "art.jsonl"),
		`{"subject": "art", "question": "Who?", "choices": ["a", "b", "c", "d"], "answer": 1}`+"\n")
	return dir
}

func TestResolve(t *testing.T) {
	t.Parallel()
	d := prompts.NewData(fixture(t))
	got, err := d.Resolve([]string{"mmlu/test:law:0", "gsm8k/test:1"})
	require.NoError(t, err)
	assert.Equal(t, []prompts.Prompt{
		{ID: "mmlu/test:law:0", Text: "Is it?\nA. yes\nB. no\nAnswer:"},
		{ID: "gsm8k/test:1", Text: "q1 long"},
	}, got)
	for _, bad := range []string{
		"gsm8k/test:2", "gsm8k/test:-1", "gsm8k/test:*", "mmlu/test:law", "mmlu/test:Law:0",
		"mmlu/test:law:1", "squad/dev:0", "nocolon",
	} {
		_, err := d.Text(bad)
		require.ErrorIs(t, err, prompts.ErrBadID, bad)
	}
	_, err = d.Text("mmlu/test:missing:0")
	require.ErrorContains(t, err, "fetch_eval_data")
}

func TestAllAndSelect(t *testing.T) {
	t.Parallel()
	all, err := prompts.NewData(fixture(t)).All()
	require.NoError(t, err)
	ids := make([]string, len(all))
	for i, p := range all {
		ids[i] = p.ID
	}
	assert.Equal(t, []string{"gsm8k/test:0", "gsm8k/test:1", "mmlu/test:art:0", "mmlu/test:law:0"}, ids)

	count := func(s string) int { return len(s) }
	a, err := prompts.Select(all, count, 3, 40, 3, 7)
	require.NoError(t, err)
	b, err := prompts.Select(all, count, 3, 40, 3, 7)
	require.NoError(t, err)
	assert.Equal(t, a, b, "same seed, same selection")
	for _, p := range a {
		assert.NotEqual(t, "gsm8k/test:0", p.ID, "out of range")
	}
	_, err = prompts.Select(all, count, 3, 40, 4, 7)
	require.ErrorContains(t, err, "only 3 prompts")
}

func TestReadIDs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "ids.txt")
	writeFile(t, p, "# header\n\ngsm8k/test:1\n mmlu/test:law:0 \n")
	ids, err := prompts.ReadIDs(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"gsm8k/test:1", "mmlu/test:law:0"}, ids)

	writeFile(t, p, "gsm8k/test:1\ngsm8k/test:1\n")
	_, err = prompts.ReadIDs(p)
	require.ErrorIs(t, err, prompts.ErrBadID)
	writeFile(t, p, "# only comments\n")
	_, err = prompts.ReadIDs(p)
	require.Error(t, err)
	_, err = prompts.ReadIDs(filepath.Join(dir, "missing"))
	require.Error(t, err)
}

// TestCommittedSet checks the committed TRG-13 set parses: 1,000 distinct ids in the grammar.
func TestCommittedSet(t *testing.T) {
	t.Parallel()
	ids, err := prompts.ReadIDs("trg13.txt")
	require.NoError(t, err)
	require.Len(t, ids, 1000)
	for _, id := range ids {
		ok := strings.HasPrefix(id, prompts.SourceGSM8K+":") || strings.HasPrefix(id, prompts.SourceMMLU+":")
		assert.True(t, ok, id)
	}
}
