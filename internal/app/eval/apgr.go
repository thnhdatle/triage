// SPDX-License-Identifier: Apache-2.0

package eval

import (
	"errors"
	"math"
	"sort"
)

var ErrNoGap = errors.New("eval: weak and strong accuracy are equal; PGR is undefined")

var ErrNoItems = errors.New("eval: no items")

type Item struct {
	Score         float64
	WeakCorrect   bool
	StrongCorrect bool
}

type Point struct {
	StrongFraction float64
	Accuracy       float64
	PGR            float64
}

// Curve returns the accuracy curve over the number of strong calls.
func Curve(items []Item) []Point {
	pts, _ := sweep(items)
	return pts
}

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
	base := 0
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

// APGR returns the area under the performance gap recovered curve.
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

func RandomExpectedAccuracy(r, accWeak, accStrong float64) float64 {
	return (1-r)*accWeak + r*accStrong
}
