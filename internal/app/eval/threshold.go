// SPDX-License-Identifier: Apache-2.0

package eval

import (
	"errors"
	"math"
	"sort"
)

// QualityFraction is the calibration rule of the cost-quality evaluation: the calibration
// threshold must keep at least this fraction of the all-strong calibration accuracy.
const QualityFraction = 0.96

// StrongRatio returns the fraction of scores with score >= threshold (the routing rule p >= t).
// It returns 0 for no scores.
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

// ThresholdForStrongRatio returns the threshold t such that the fraction of scores >= t is as close as
// possible to ratio. Only thresholds that equal a score (or lie just above the maximum, for ratio 0)
// are candidates, so ties are never split; when two candidates are equally close the one with the
// smaller strong ratio wins (the target is not exceeded). ratio is clamped to [0,1]; no scores yield
// NaN.
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
			continue // not a group end
		}
		diff := math.Abs(float64(i+1)/float64(n) - ratio)
		if diff < bestDiff {
			best, bestDiff = sorted[i], diff
		}
	}
	return best
}

// ErrNoCalibrationItems is returned by CalibrationThreshold for an empty item list.
var ErrNoCalibrationItems = errors.New("eval: no calibration items")

// CalibrationThreshold applies the calibration rule: the lowest strong-call ratio on the calibration items
// whose accuracy is at least QualityFraction times the all-strong accuracy, restricted to ratios a
// threshold can realise (tie-group boundaries). It returns that threshold and the realised ratio.
// A ratio of 0 yields a threshold just above the highest score.
func CalibrationThreshold(items []Item) (threshold, ratio float64, err error) {
	return CalibrationThresholdAt(items, QualityFraction)
}

// CalibrationThresholdAt is CalibrationThreshold with a configurable fraction of the all-strong
// accuracy (threshold_rule.min_accuracy_of_all_strong).
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
	// Unreachable: k = N always qualifies.
	return order[n-1], 1, nil
}
