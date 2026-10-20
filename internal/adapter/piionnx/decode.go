// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"fmt"
	"math"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// window is a slice [start, start+n) of the content tokens scanned in one
// sequence of the batch, framed by [CLS] and [SEP].
type window struct{ start, n int }

// windows covers n content tokens with windows of at most size tokens that
// overlap by overlap tokens. size > overlap >= 0.
func windows(n, size, overlap int) []window {
	if n == 0 {
		return nil
	}
	stride := size - overlap
	var ws []window
	for start := 0; ; start += stride {
		w := window{start: start, n: min(size, n-start)}
		ws = append(ws, w)
		if start+w.n >= n {
			return ws
		}
	}
}

// owner returns, for every content token, the index of the window whose
// prediction is used: the one where the token is farthest from an edge, so it
// has the most context on both sides. Ties go to the earlier window.
func owner(n int, ws []window) []int {
	own := make([]int, n)
	best := make([]int, n)
	for i := range best {
		best[i] = -1
	}
	for wi, w := range ws {
		for j := w.start; j < w.start+w.n; j++ {
			d := min(j-w.start, w.start+w.n-1-j)
			if d > best[j] {
				best[j], own[j] = d, wi
			}
		}
	}
	return own
}

// batch holds the model inputs for all windows, padded to the same length.
type batch struct {
	rows, cols int
	ids, mask  []int64
}

// padID fills positions after [SEP]; the attention mask hides them, and
// [PAD] is id 0 in BERT vocabularies.
const padID = 0

func newBatch(ids []int, ws []window, cls, sep int) batch {
	cols := 0
	for _, w := range ws {
		cols = max(cols, w.n+2)
	}
	b := batch{rows: len(ws), cols: cols, ids: make([]int64, len(ws)*cols), mask: make([]int64, len(ws)*cols)}
	for r, w := range ws {
		row := b.ids[r*cols : (r+1)*cols]
		m := b.mask[r*cols : (r+1)*cols]
		row[0] = int64(cls)
		for k := range w.n {
			row[1+k] = int64(ids[w.start+k])
		}
		row[w.n+1] = int64(sep)
		for k := w.n + 2; k < cols; k++ {
			row[k] = padID
		}
		for k := range w.n + 2 {
			m[k] = 1
		}
	}
	return b
}

// tokenProbs turns the logits [rows, cols, labels] into per-content-token
// label probabilities, each taken from the token's owning window.
func tokenProbs(logits []float32, shape []int64, b batch, ws []window, nLabels int) ([][]float64, error) {
	want := []int64{int64(b.rows), int64(b.cols), int64(nLabels)}
	if len(shape) != 3 || shape[0] != want[0] || shape[1] != want[1] || shape[2] != want[2] {
		return nil, fmt.Errorf("piionnx: logits shape %v, want %v (labels from config.json)", shape, want)
	}
	n := 0
	for _, w := range ws {
		n = max(n, w.start+w.n)
	}
	own := owner(n, ws)
	probs := make([][]float64, n)
	for j := range n {
		w := ws[own[j]]
		off := (own[j]*b.cols + 1 + j - w.start) * nLabels
		probs[j] = softmax(logits[off : off+nLabels])
	}
	return probs, nil
}

func softmax(x []float32) []float64 {
	m := math.Inf(-1)
	for _, v := range x {
		m = math.Max(m, float64(v))
	}
	out := make([]float64, len(x))
	sum := 0.0
	for i, v := range x {
		out[i] = math.Exp(float64(v) - m)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

// word is a WordPiece word: a first token and its continuations.
type word struct {
	start, end int
	label      int
	score      float64
}

// words groups tokens into words and labels each with the argmax of its
// tokens' averaged probabilities (HF aggregation "average").
func words(spans []span, probs [][]float64) []word {
	var out []word
	for i := 0; i < len(spans); {
		j := i + 1
		for j < len(spans) && spans[j].cont {
			j++
		}
		avg := make([]float64, len(probs[i]))
		for k := i; k < j; k++ {
			for l, p := range probs[k] {
				avg[l] += p
			}
		}
		best := 0
		for l := range avg {
			if avg[l] > avg[best] {
				best = l
			}
		}
		out = append(out, word{start: spans[i].start, end: spans[j-1].end, label: best, score: avg[best] / float64(j-i)})
		i = j
	}
	return out
}

// entity is a run of words forming one named entity.
type entity struct {
	typ        string
	raw        string
	start, end int
	sum        float64
	n          int
}

// entities decodes BIO labels over words: B-X starts an entity, I-X extends
// an open X entity and otherwise starts one (as HF's "simple" grouping), and
// O or a different type closes it. Entities of ignored types (MISC) are
// decoded but dropped, as are entities whose mean word score is below
// minScore. Findings are ordered by start offset.
func entities(text string, ws []word, labels []label, minScore float64) []privacy.Finding {
	var out []privacy.Finding
	var cur *entity
	flush := func() {
		if cur != nil && cur.typ != "" && cur.sum/float64(cur.n) >= minScore {
			out = append(out, privacy.Finding{
				DetectorID: ID,
				Type:       cur.typ,
				Start:      cur.start,
				End:        cur.end,
				Confidence: cur.sum / float64(cur.n),
				Value:      text[cur.start:cur.end],
			})
		}
		cur = nil
	}
	for _, w := range ws {
		l := labels[w.label]
		switch {
		case l.tag == tagO:
			flush()
		case l.tag == tagI && cur != nil && cur.raw == l.entity:
			cur.end = w.end
			cur.sum += w.score
			cur.n++
		default:
			flush()
			cur = &entity{typ: l.typ, raw: l.entity, start: w.start, end: w.end, sum: w.score, n: 1}
		}
	}
	flush()
	return out
}
