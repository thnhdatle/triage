// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

func TestWindows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		n, size, overlap int
		want             []window
	}{
		{name: "empty", n: 0, size: 4, overlap: 1},
		{name: "fits", n: 3, size: 4, overlap: 1, want: []window{{0, 3}}},
		{name: "exact", n: 4, size: 4, overlap: 1, want: []window{{0, 4}}},
		{name: "two", n: 5, size: 4, overlap: 1, want: []window{{0, 4}, {3, 2}}},
		{name: "three", n: 10, size: 4, overlap: 1, want: []window{{0, 4}, {3, 4}, {6, 4}}},
		{name: "no overlap", n: 8, size: 4, overlap: 0, want: []window{{0, 4}, {4, 4}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, windows(tc.n, tc.size, tc.overlap))
		})
	}
}

func TestWindows_Property(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(1, 64).Draw(t, "size")
		overlap := rapid.IntRange(0, size-1).Draw(t, "overlap")
		n := rapid.IntRange(1, 1000).Draw(t, "n")
		ws := windows(n, size, overlap)
		require.Equal(t, 0, ws[0].start)
		require.Equal(t, n, ws[len(ws)-1].start+ws[len(ws)-1].n)
		for i, w := range ws {
			require.LessOrEqual(t, w.n, size)
			require.Positive(t, w.n)
			if i > 0 {
				prevEnd := ws[i-1].start + ws[i-1].n
				require.Equal(t, overlap, prevEnd-w.start)
			}
		}
		own := owner(n, ws)
		for j, o := range own {
			w := ws[o]
			require.True(t, w.start <= j && j < w.start+w.n, "token %d owned by a window that holds it", j)
		}
	})
}

func TestOwner_PrefersCentralWindow(t *testing.T) {
	t.Parallel()
	ws := []window{{0, 4}, {2, 4}}
	assert.Equal(t, []int{0, 0, 0, 1, 1, 1}, owner(6, ws))
}

func TestNewBatch(t *testing.T) {
	t.Parallel()
	ws := []window{{0, 3}, {2, 2}}
	b := newBatch([]int{11, 12, 13, 14}, ws, 101, 102)
	assert.Equal(t, 2, b.rows)
	assert.Equal(t, 5, b.cols)
	assert.Equal(t, []int64{101, 11, 12, 13, 102, 101, 13, 14, 102, 0}, b.ids)
	assert.Equal(t, []int64{1, 1, 1, 1, 1, 1, 1, 1, 1, 0}, b.mask)
}

func TestTokenProbs_UsesOwningWindow(t *testing.T) {
	t.Parallel()
	ws := []window{{0, 2}, {1, 1}}
	b := newBatch([]int{7, 8}, ws, 1, 2)
	logits := []float32{
		0, 0, 5, 0, 0, 5, 0, 0,
		0, 0, 9, 0, 0, 0, 0, 0,
	}
	probs, err := tokenProbs(logits, []int64{2, 4, 2}, b, ws, 2)
	require.NoError(t, err)
	require.Len(t, probs, 2)
	assert.Greater(t, probs[0][0], 0.99, "token 0 from window 0")
	assert.Greater(t, probs[1][1], 0.99, "token 1 from window 0, not window 1")
}

func TestTokenProbs_ShapeMismatch(t *testing.T) {
	t.Parallel()
	ws := []window{{0, 1}}
	b := newBatch([]int{7}, ws, 1, 2)
	for _, shape := range [][]int64{{1, 3}, {2, 3, 2}, {1, 4, 2}, {1, 3, 3}} {
		_, err := tokenProbs(make([]float32, 18), shape, b, ws, 2)
		require.Error(t, err, "%v", shape)
	}
}

func TestSoftmax(t *testing.T) {
	t.Parallel()
	p := softmax([]float32{1000, 1000, -math.MaxFloat32})
	assert.InDeltaSlice(t, []float64{0.5, 0.5, 0}, p, 1e-12, "stable for large logits")
}

func TestWords_AverageAcrossPieces(t *testing.T) {
	t.Parallel()
	spans := []span{{0, 2, false}, {2, 4, true}, {5, 6, false}}
	probs := [][]float64{{0.1, 0.9}, {0.5, 0.5}, {0.6, 0.4}}
	got := words(spans, probs)
	want := []word{{start: 0, end: 4, label: 1, score: 0.7}, {start: 5, end: 6, label: 0, score: 0.6}}
	assert.Equal(t, want, roundWords(got))
}

func roundWords(ws []word) []word {
	for i := range ws {
		ws[i].score = math.Round(ws[i].score*1e9) / 1e9
	}
	return ws
}

func TestEntities_BIO(t *testing.T) {
	t.Parallel()
	labels := []label{
		{tag: tagO},
		{tag: tagB, entity: "MISC"},
		{tag: tagI, entity: "MISC"},
		{tag: tagB, entity: "PER", typ: TypePerson},
		{tag: tagI, entity: "PER", typ: TypePerson},
		{tag: tagB, entity: "ORG", typ: TypeOrganization},
		{tag: tagI, entity: "ORG", typ: TypeOrganization},
		{tag: tagB, entity: "LOC", typ: TypeLocation},
		{tag: tagI, entity: "LOC", typ: TypeLocation},
	}
	const text = "aa bb cc dd ee"
	at := func(i, lbl int, score float64) word {
		return word{start: 3 * i, end: 3*i + 2, label: lbl, score: score}
	}
	tests := []struct {
		name  string
		words []word
		min   float64
		want  []privacy.Finding
	}{
		{name: "none", words: []word{at(0, lblO, 1)}},
		{
			name: "B I I", words: []word{at(0, lblBPER, 1), at(1, lblIPER, 0.8), at(2, lblIPER, 0.9)},
			want: []privacy.Finding{{DetectorID: ID, Type: TypePerson, Start: 0, End: 8, Confidence: 0.9, Value: "aa bb cc"}},
		},
		{
			name: "I of another type starts a new entity", words: []word{at(0, lblBPER, 1), at(1, lblILOC, 1)},
			want: []privacy.Finding{
				{DetectorID: ID, Type: TypePerson, Start: 0, End: 2, Confidence: 1, Value: "aa"},
				{DetectorID: ID, Type: TypeLocation, Start: 3, End: 5, Confidence: 1, Value: "bb"},
			},
		},
		{
			name: "O closes", words: []word{at(0, lblBORG, 1), at(1, lblO, 1), at(2, lblIORG, 1)},
			want: []privacy.Finding{
				{DetectorID: ID, Type: TypeOrganization, Start: 0, End: 2, Confidence: 1, Value: "aa"},
				{DetectorID: ID, Type: TypeOrganization, Start: 6, End: 8, Confidence: 1, Value: "cc"},
			},
		},
		{
			name: "MISC run dropped and closes", words: []word{at(0, lblBMISC, 1), at(1, lblIMISC, 1), at(2, lblIPER, 1)},
			want: []privacy.Finding{{DetectorID: ID, Type: TypePerson, Start: 6, End: 8, Confidence: 1, Value: "cc"}},
		},
		{
			name: "mean below min score dropped", min: 0.8,
			words: []word{at(0, lblBLOC, 0.9), at(1, lblILOC, 0.6), at(3, lblBLOC, 0.8)},
			want:  []privacy.Finding{{DetectorID: ID, Type: TypeLocation, Start: 9, End: 11, Confidence: 0.8, Value: "dd"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := entities(text, tc.words, labels, tc.min)
			require.Len(t, got, len(tc.want))
			for i := range got {
				assert.InDelta(t, tc.want[i].Confidence, got[i].Confidence, 1e-12)
				got[i].Confidence = tc.want[i].Confidence
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
