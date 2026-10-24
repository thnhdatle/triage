// SPDX-License-Identifier: Apache-2.0

package eval_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/eval"
)

func TestThreshold_ThresholdForStrongRatio_Cases(t *testing.T) {
	t.Parallel()
	distinct := []float64{0.3, 0.9, 0.5, 0.7}
	ties := []float64{0.5, 0.1, 0.5, 0.1}
	tests := []struct {
		name   string
		scores []float64
		ratio  float64
		want   float64
	}{
		{name: "half", scores: distinct, ratio: 0.5, want: 0.7},
		{name: "zero", scores: distinct, ratio: 0, want: math.Nextafter(0.9, 2)},
		{name: "one", scores: distinct, ratio: 1, want: 0.3},
		{name: "closest", scores: distinct, ratio: 0.4, want: 0.7},
		{name: "equal distance prefers not exceeding", scores: distinct, ratio: 0.375, want: 0.9},
		{name: "ties not split", scores: ties, ratio: 0.25, want: math.Nextafter(0.5, 2)},
		{name: "ties group", scores: ties, ratio: 0.6, want: 0.5},
		{name: "clamped high", scores: distinct, ratio: 7, want: 0.3},
		{name: "clamped low", scores: distinct, ratio: -1, want: math.Nextafter(0.9, 2)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			orig := append([]float64(nil), tc.scores...)
			assert.InDelta(t, tc.want, eval.ThresholdForStrongRatio(tc.scores, tc.ratio), 0)
			assert.Equal(t, orig, tc.scores)
		})
	}
	assert.True(t, math.IsNaN(eval.ThresholdForStrongRatio(nil, 0.5)))
}

func TestThreshold_StrongRatio_Cases(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 0.5, eval.StrongRatio([]float64{0.1, 0.5, 0.7, 0.2}, 0.5), 0)
	assert.InDelta(t, 0, eval.StrongRatio(nil, 0.5), 0)
}

func TestThreshold_CalibrationThreshold_Cases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		items     []eval.Item
		wantThr   float64
		wantRatio float64
	}{
		{
			name: "one strong call is enough",
			items: []eval.Item{
				{Score: 0.9, WeakCorrect: false, StrongCorrect: true},
				{Score: 0.8, WeakCorrect: true, StrongCorrect: true},
				{Score: 0.7, WeakCorrect: true, StrongCorrect: true},
				{Score: 0.6, WeakCorrect: false, StrongCorrect: false},
			},
			wantThr: 0.9, wantRatio: 0.25,
		},
		{
			name: "ties force all strong",
			items: []eval.Item{
				{Score: 0.5, WeakCorrect: false, StrongCorrect: true},
				{Score: 0.5, WeakCorrect: false, StrongCorrect: true},
			},
			wantThr: 0.5, wantRatio: 1,
		},
		{
			name: "weak already good enough",
			items: []eval.Item{
				{Score: 0.4, WeakCorrect: true, StrongCorrect: true},
				{Score: 0.2, WeakCorrect: true, StrongCorrect: true},
			},
			wantThr:   math.Nextafter(0.4, 2),
			wantRatio: 0,
		},
		{
			name: "needs most items",
			items: []eval.Item{
				{Score: 0.9, WeakCorrect: true, StrongCorrect: true},
				{Score: 0.8, WeakCorrect: true, StrongCorrect: true},
				{Score: 0.2, WeakCorrect: false, StrongCorrect: true},
				{Score: 0.1, WeakCorrect: false, StrongCorrect: true},
			},
			wantThr: 0.1, wantRatio: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			thr, ratio, err := eval.CalibrationThreshold(tc.items)
			require.NoError(t, err)
			assert.InDelta(t, tc.wantThr, thr, 0)
			assert.InDelta(t, tc.wantRatio, ratio, 1e-12)
		})
	}
}

func TestThreshold_CalibrationThreshold_Empty(t *testing.T) {
	t.Parallel()
	_, _, err := eval.CalibrationThreshold(nil)
	require.ErrorIs(t, err, eval.ErrNoCalibrationItems)
}

func TestThreshold_CalibrationThresholdAt_Fraction(t *testing.T) {
	t.Parallel()
	items := []eval.Item{
		{Score: 0.9, WeakCorrect: false, StrongCorrect: true},
		{Score: 0.8, WeakCorrect: false, StrongCorrect: true},
		{Score: 0.7, WeakCorrect: true, StrongCorrect: true},
		{Score: 0.6, WeakCorrect: true, StrongCorrect: true},
	}
	tests := []struct {
		fraction  float64
		wantThr   float64
		wantRatio float64
	}{
		{fraction: 0.5, wantThr: math.Nextafter(0.9, 2), wantRatio: 0},
		{fraction: 0.75, wantThr: 0.9, wantRatio: 0.25},
		{fraction: 1, wantThr: 0.8, wantRatio: 0.5},
	}
	for _, tc := range tests {
		thr, ratio, err := eval.CalibrationThresholdAt(items, tc.fraction)
		require.NoError(t, err)
		assert.InDelta(t, tc.wantThr, thr, 0, "fraction %v", tc.fraction)
		assert.InDelta(t, tc.wantRatio, ratio, 1e-12, "fraction %v", tc.fraction)
	}
	thr, ratio, err := eval.CalibrationThresholdAt(items, eval.QualityFraction)
	require.NoError(t, err)
	wantThr, wantRatio, err := eval.CalibrationThreshold(items)
	require.NoError(t, err)
	assert.InDelta(t, wantThr, thr, 0)
	assert.InDelta(t, wantRatio, ratio, 0)
}
