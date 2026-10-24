// SPDX-License-Identifier: Apache-2.0

package eval_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/app/eval"
)

var twoItems = []eval.Item{
	{Score: 0.9, WeakCorrect: false, StrongCorrect: true},
	{Score: 0.1, WeakCorrect: true, StrongCorrect: true},
}

func TestAPGR_APGR_HandComputed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		items []eval.Item
		want  float64
	}{
		{name: "good router", items: twoItems, want: 0.75},
		{
			name: "inverted router",
			items: []eval.Item{
				{Score: 0.1, WeakCorrect: false, StrongCorrect: true},
				{Score: 0.9, WeakCorrect: true, StrongCorrect: true},
			},
			want: 0.25,
		},
		{
			name: "all ties average over the group",
			items: []eval.Item{
				{Score: 0.5, WeakCorrect: false, StrongCorrect: true},
				{Score: 0.5, WeakCorrect: true, StrongCorrect: true},
			},
			want: 0.5,
		},
		{name: "single item", items: []eval.Item{{Score: 0.3, StrongCorrect: true}}, want: 0.5},
		{
			name: "strong worse than weak on some items",
			items: []eval.Item{
				{Score: 0.8, WeakCorrect: true, StrongCorrect: false},
				{Score: 0.6, WeakCorrect: false, StrongCorrect: true},
				{Score: 0.4, WeakCorrect: false, StrongCorrect: true},
				{Score: 0.2, WeakCorrect: true, StrongCorrect: true},
			},
			want: 0.25 * ((0-1)/2.0 + (-1+0)/2.0 + (0+1)/2.0 + 1),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := eval.APGR(tc.items)
			require.NoError(t, err)
			assert.InDelta(t, tc.want, got, 1e-12)
		})
	}
}

func TestAPGR_APGR_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		items []eval.Item
		want  error
	}{
		{name: "empty", items: nil, want: eval.ErrNoItems},
		{name: "equal accuracy", items: []eval.Item{{Score: 1, WeakCorrect: true, StrongCorrect: true}}, want: eval.ErrNoGap},
		{
			name: "equal accuracy different items",
			items: []eval.Item{
				{Score: 1, WeakCorrect: true},
				{Score: 0, StrongCorrect: true},
			},
			want: eval.ErrNoGap,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := eval.APGR(tc.items)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestAPGR_Curve_Points(t *testing.T) {
	t.Parallel()
	got := eval.Curve(twoItems)
	want := []eval.Point{
		{StrongFraction: 0, Accuracy: 0.5, PGR: 0},
		{StrongFraction: 0.5, Accuracy: 1, PGR: 1},
		{StrongFraction: 1, Accuracy: 1, PGR: 1},
	}
	assert.Equal(t, want, got)
	assert.Nil(t, eval.Curve(nil))

	flat := eval.Curve([]eval.Item{{Score: 1, WeakCorrect: true, StrongCorrect: true}})
	require.Len(t, flat, 2)
	assert.True(t, math.IsNaN(flat[0].PGR))
}

func TestAPGR_Curve_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := []eval.Item{{Score: 0.1, StrongCorrect: true}, {Score: 0.9}}
	_ = eval.Curve(in)
	assert.InDelta(t, 0.1, in[0].Score, 0)
}

func TestAPGR_RandomExpectedAccuracy_Values(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		r, weak, str float64
		want         float64
	}{
		{name: "zero", r: 0, weak: 0.4, str: 0.8, want: 0.4},
		{name: "one", r: 1, weak: 0.4, str: 0.8, want: 0.8},
		{name: "half", r: 0.5, weak: 0.4, str: 0.8, want: 0.6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tc.want, eval.RandomExpectedAccuracy(tc.r, tc.weak, tc.str), 1e-12)
		})
	}
}

func itemsGen() *rapid.Generator[[]eval.Item] {
	return rapid.SliceOfN(rapid.Custom(func(t *rapid.T) eval.Item {
		return eval.Item{
			Score:         float64(rapid.IntRange(0, 4).Draw(t, "score")) / 4,
			WeakCorrect:   rapid.Bool().Draw(t, "weak"),
			StrongCorrect: rapid.Bool().Draw(t, "strong"),
		}
	}), 1, 30)
}

func TestAPGR_Curve_EndpointsProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		items := itemsGen().Draw(t, "items")
		pts := eval.Curve(items)
		require.Len(t, pts, len(items)+1)
		if math.IsNaN(pts[0].PGR) {
			return
		}
		assert.InDelta(t, 0, pts[0].PGR, 1e-12)
		assert.InDelta(t, 1, pts[len(items)].PGR, 1e-12)
	})
}

func TestAPGR_APGR_OracleDominatesProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		items := itemsGen().Draw(t, "items")
		oracle := make([]eval.Item, len(items))
		for i, it := range items {
			oracle[i] = it
			oracle[i].Score = float64(boolInt(it.StrongCorrect) - boolInt(it.WeakCorrect))
		}
		pts := eval.Curve(items)
		if pts[len(pts)-1].Accuracy <= pts[0].Accuracy {
			return
		}
		got, err := eval.APGR(items)
		best, errBest := eval.APGR(oracle)
		require.NoError(t, err)
		require.NoError(t, errBest)
		assert.LessOrEqual(t, got, best+1e-9)
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
