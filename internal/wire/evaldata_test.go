// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvalData_ParseItemID(t *testing.T) {
	t.Parallel()
	ok := map[string]itemID{
		"gsm8k/test:0":              {source: srcGSM8K, row: 0},
		"gsm8k/test:1318":           {source: srcGSM8K, row: 1318},
		"gsm8k/test:*":              {source: srcGSM8K, row: -1},
		"mmlu/test:anatomy:12":      {source: srcMMLU, subject: "anatomy", row: 12},
		"mmlu/test:college_bio_2:0": {source: srcMMLU, subject: "college_bio_2", row: 0},
	}
	for id, want := range ok {
		got, err := parseItemID(id)
		require.NoError(t, err, id)
		assert.Equal(t, want, got, id)
	}
	for _, id := range []string{
		"", "gsm8k/test", "gsm8k/test:x", "gsm8k/test:-1", "gsm8k/train:1", "mmlu/test:1",
		"mmlu/test:../etc:1", "mmlu/test:Anatomy:1", "mmlu/test:anatomy:*", "other:1",
	} {
		_, err := parseItemID(id)
		assert.Error(t, err, id)
	}
}

func TestEvalData_ReadItemIDs(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "set.txt")
	content := "# header\n\ngsm8k/test:3\n  mmlu/test:anatomy:1  \ngsm8k/test:3\n# end\n"
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	ids, err := readItemIDs(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"gsm8k/test:3", "mmlu/test:anatomy:1"}, ids)

	empty := filepath.Join(t.TempDir(), "empty.txt")
	require.NoError(t, os.WriteFile(empty, []byte("# nothing\n"), 0o600))
	_, err = readItemIDs(empty)
	require.Error(t, err)
	_, err = readItemIDs(filepath.Join(t.TempDir(), "missing.txt"))
	require.Error(t, err)
}

func TestEvalData_RoutedStrong(t *testing.T) {
	t.Parallel()
	assert.True(t, routedStrong(2, 5), "pinned strong stays strong")
	assert.False(t, routedStrong(-1, -5), "pinned weak stays weak")
	assert.True(t, routedStrong(0.5, 0.5))
	assert.False(t, routedStrong(0.49, 0.5))
	assert.InDelta(t, 0.5, strongShare([]float64{2, -1, 0.9, 0.1}, 0.5), 1e-12)
	assert.InDelta(t, 0, strongShare(nil, 0.5), 0)
}

func TestEvalData_SweepThresholds(t *testing.T) {
	t.Parallel()
	scores := make([]float64, 100)
	for i := range scores {
		scores[i] = float64(i) / 100
	}
	ts := sweepThresholds(scores, 0.4321)
	assert.Contains(t, ts, 0.4321, "the chosen threshold is part of the sweep")
	assert.Len(t, ts, 22, "21 grid points (0..1 step 0.05) plus the chosen threshold")
	for i := 1; i < len(ts); i++ {
		assert.Greater(t, ts[i-1], ts[i], "strictly descending thresholds, ascending strong ratio")
	}
	assert.Greater(t, ts[0], 0.99, "ratio 0 lies above the highest score")
	assert.InDelta(t, 0, ts[len(ts)-1], 1e-12, "ratio 1 is the lowest score")
	assert.False(t, math.IsNaN(ts[0]))
	assert.Len(t, sweepThresholds(scores, 0.45), 21, "a grid threshold is not repeated")
}
