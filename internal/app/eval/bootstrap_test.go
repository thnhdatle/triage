// SPDX-License-Identifier: Apache-2.0

package eval_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/thnhdatle/triage/internal/app/eval"
)

func meanStat(vals []float64) func(idx []int) float64 {
	return func(idx []int) float64 {
		s := 0.0
		for _, i := range idx {
			s += vals[i]
		}
		return s / float64(len(idx))
	}
}

func TestBootstrap_Bootstrap_Deterministic(t *testing.T) {
	t.Parallel()
	vals := []float64{1, 0, 1, 1, 0, 1, 0, 0, 1, 1}
	spread := []float64{0.1, 0.35, 0.8, 0.2, 0.95, 0.4, 0.6, 0.15, 0.7, 0.55, 0.3, 0.85}
	lo1, hi1 := eval.Bootstrap(len(vals), 2000, 42, meanStat(vals))
	lo2, hi2 := eval.Bootstrap(len(vals), 2000, 42, meanStat(vals))
	lo3, hi3 := eval.Bootstrap(len(spread), 2000, 42, meanStat(spread))
	lo4, hi4 := eval.Bootstrap(len(spread), 2000, 43, meanStat(spread))
	assert.InDelta(t, lo1, lo2, 0)
	assert.InDelta(t, hi1, hi2, 0)
	assert.NotEqual(t, []float64{lo3, hi3}, []float64{lo4, hi4})
	assert.Less(t, lo1, 0.6)
	assert.Greater(t, hi1, 0.6)
	assert.GreaterOrEqual(t, lo1, 0.0)
	assert.LessOrEqual(t, hi1, 1.0)
}

func TestBootstrap_Bootstrap_Cases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		n, res    int
		stat      func([]int) float64
		wantLo    float64
		wantHi    float64
		wantNaNLo bool
	}{
		{name: "constant", n: 5, res: 100, stat: func([]int) float64 { return 3 }, wantLo: 3, wantHi: 3},
		{name: "zero n", n: 0, res: 100, stat: func([]int) float64 { return 3 }, wantNaNLo: true},
		{name: "zero resamples", n: 3, res: 0, stat: func([]int) float64 { return 3 }, wantNaNLo: true},
		{name: "all nan", n: 3, res: 10, stat: func([]int) float64 { return math.NaN() }, wantNaNLo: true},
		{name: "single resample", n: 3, res: 1, stat: func([]int) float64 { return 2 }, wantLo: 2, wantHi: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lo, hi := eval.Bootstrap(tc.n, tc.res, 1, tc.stat)
			if tc.wantNaNLo {
				assert.True(t, math.IsNaN(lo) && math.IsNaN(hi))
				return
			}
			assert.InDelta(t, tc.wantLo, lo, 0)
			assert.InDelta(t, tc.wantHi, hi, 0)
		})
	}
}

func TestBootstrap_Bootstrap_PercentileIndices(t *testing.T) {
	t.Parallel()
	// stat returns 0..9999 in call order, so the sorted sample is the identity and the interval is
	// the 2.5th/97.5th nearest-rank elements.
	calls := -1
	lo, hi := eval.Bootstrap(4, 10000, 7, func([]int) float64 {
		calls++
		return float64(calls)
	})
	assert.InDelta(t, 250, lo, 0)
	assert.InDelta(t, 9749, hi, 0)
}
