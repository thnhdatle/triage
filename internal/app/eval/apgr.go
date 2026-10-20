// SPDX-License-Identifier: Apache-2.0

package eval

import (
	"errors"
	"math"
	"sort"
)

// ErrNoGap is returned by APGR when weak and strong have equal accuracy, so PGR is undefined.
var ErrNoGap = errors.New("eval: weak and strong accuracy are equal; PGR is undefined")

// ErrNoItems is returned when a computation needs at least one item.
var ErrNoItems = errors.New("eval: no items")

// Item is one evaluated prompt: the router's score and the correctness of each model's answer.
type Item struct {
	// Score is the router score; higher means more likely to need the strong model.
	Score         float64
	WeakCorrect   bool
	StrongCorrect bool
}

// Point is one point of the cost-quality curve.
type Point struct {
	// StrongFraction is k/N, the fraction of items routed to strong.
	StrongFraction float64
	// Accuracy is the (tie-averaged) accuracy when the top k scored items go to strong.
	Accuracy float64
	// PGR is the performance gap recovered; NaN when acc_strong == acc_weak.
	PGR float64
}

// Curve returns the N+1 points k = 0..N of the router's curve, see the package documentation for the
// tie handling. It returns nil for no items.
func Curve(items []Item) []Point {
	pts, _ := sweep(items)
	return pts
}

// sweep builds the curve and reports, for each k, whether k is a tie-group boundary (a strong count
// that a threshold can realise exactly).
func sweep(items []Item) ([]Point, []bool) {
	n := len(items)
	if n == 0 {
		return nil, nil
	}
	order := make([]Item, n)
	copy(order, items)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Score > order[j].Score })

	weak, strong := 0, 0
	for _, it := range order {
		weak += b2i(it.WeakCorrect)
		strong += b2i(it.StrongCorrect)
	}
	fn := float64(n)
	accW, accS := float64(weak)/fn, float64(strong)/fn

	pts := make([]Point, 0, n+1)
	boundary := make([]bool, 0, n+1)
	pts = append(pts, makePoint(0, fn, accW, accW, accS))
	boundary = append(boundary, true)
	base := 0 // integer gain of all fully routed groups
	for start := 0; start < n; {
		end := start
		gain := 0
		for end < n && order[end].Score == order[start].Score {
			gain += b2i(order[end].StrongCorrect) - b2i(order[end].WeakCorrect)
			end++
		}
		m := end - start
		for j := 1; j <= m; j++ {
			acc := (float64(weak) + float64(base) + float64(gain)*float64(j)/float64(m)) / fn
			pts = append(pts, makePoint(start+j, fn, acc, accW, accS))
			boundary = append(boundary, j == m)
		}
		base += gain
		start = end
	}
	return pts, boundary
}

func makePoint(k int, n, acc, accW, accS float64) Point {
	pgr := math.NaN()
	if accS != accW {
		pgr = (acc - accW) / (accS - accW)
	}
	return Point{StrongFraction: float64(k) / n, Accuracy: acc, PGR: pgr}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// APGR returns the average performance gap recovered of the router that produced items' scores: the
// trapezoid area under the PGR curve over the strong-call fraction (package documentation). It returns
// ErrNoItems for an empty list and ErrNoGap when acc_strong == acc_weak.
func APGR(items []Item) (float64, error) {
	pts := Curve(items)
	if pts == nil {
		return 0, ErrNoItems
	}
	if math.IsNaN(pts[0].PGR) {
		return 0, ErrNoGap
	}
	area := 0.0
	for i := 1; i < len(pts); i++ {
		area += (pts[i].StrongFraction - pts[i-1].StrongFraction) * (pts[i].PGR + pts[i-1].PGR) / 2
	}
	return area, nil
}

// RandomExpectedAccuracy is the expected accuracy of a random router that sends fraction r of the
// calls to strong: (1-r)*accWeak + r*accStrong.
func RandomExpectedAccuracy(r, accWeak, accStrong float64) float64 {
	return (1-r)*accWeak + r*accStrong
}
