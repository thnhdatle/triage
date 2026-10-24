// SPDX-License-Identifier: Apache-2.0

package eval

import (
	"errors"
	"math"
	"sort"
)

const QualityFraction = 0.96

func StrongRatio(scores []float64, threshold float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	n := 0
	for _, s := range scores {
		if s >= threshold {
			n++
		}
	}
	return float64(n) / float64(len(scores))
}

// ThresholdForStrongRatio returns the threshold that sends about ratio of the scores to the strong model.
func ThresholdForStrongRatio(scores []float64, ratio float64) float64 {
	n := len(scores)
	if n == 0 {
		return math.NaN()
	}
	ratio = math.Min(1, math.Max(0, ratio))
	sorted := make([]float64, n)
	copy(sorted, scores)
	sort.Sort(sort.Reverse(sort.Float64Slice(sorted)))

	best := math.Nextafter(sorted[0], math.Inf(1))
	bestDiff := math.Abs(ratio)
	for i := 0; i < n; i++ {
		if i+1 < n && sorted[i+1] == sorted[i] {
			continue
		}
		diff := math.Abs(float64(i+1)/float64(n) - ratio)
		if diff < bestDiff {
			best, bestDiff = sorted[i], diff
		}
	}
	return best
}

var ErrNoCalibrationItems = errors.New("eval: no calibration items")

func CalibrationThreshold(items []Item) (threshold, ratio float64, err error) {
	return CalibrationThresholdAt(items, QualityFraction)
}

func CalibrationThresholdAt(items []Item, fraction float64) (threshold, ratio float64, err error) {
	pts, boundary := sweep(items)
	if pts == nil {
		return 0, 0, ErrNoCalibrationItems
	}
	n := len(items)
	order := make([]float64, n)
	for i, it := range items {
		order[i] = it.Score
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(order)))

	need := fraction * pts[n].Accuracy
	for k, p := range pts {
		if !boundary[k] || p.Accuracy < need-1e-12 {
			continue
		}
		if k == 0 {
			return math.Nextafter(order[0], math.Inf(1)), 0, nil
		}
		return order[k-1], p.StrongFraction, nil
	}
	return order[n-1], 1, nil
}
