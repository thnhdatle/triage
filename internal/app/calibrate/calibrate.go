// SPDX-License-Identifier: Apache-2.0

package calibrate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

type Result struct {
	Strategy      string
	Threshold     difficulty.Threshold
	AchievedRatio float64
	N             int
}

// Snippet renders the YAML routes snippet for the configuration.
func (r Result) Snippet(route string) string {
	return "routes:\n  - name: " + route + "\n    threshold: " +
		strconv.FormatFloat(float64(r.Threshold), 'g', -1, 64) + "\n"
}

// Calibrate picks the route threshold whose strong-call share is closest to ratio.
func Calibrate(
	ctx context.Context, strategy difficulty.Strategy, sample []chat.Request, strongRatio float64,
) (Result, error) {
	if !(strongRatio >= 0 && strongRatio <= 1) {
		return Result{}, fmt.Errorf("calibrate: strong ratio %v outside [0,1]", strongRatio)
	}
	if len(sample) == 0 {
		return Result{}, errors.New("calibrate: empty sample")
	}
	scores := make([]float64, 0, len(sample))
	for i, req := range sample {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("calibrate: %w", err)
		}
		s, err := strategy.Score(ctx, req)
		if err != nil {
			return Result{}, fmt.Errorf("calibrate: score sample %d with %s: %w", i, strategy.Name(), err)
		}
		if err := s.Validate(); err != nil {
			return Result{}, fmt.Errorf("calibrate: sample %d with %s: %w", i, strategy.Name(), err)
		}
		scores = append(scores, s.P)
	}
	t := thresholdForRatio(scores, strongRatio)
	return Result{
		Strategy:      strategy.Name(),
		Threshold:     difficulty.Threshold(t),
		AchievedRatio: ratioAbove(scores, t),
		N:             len(scores),
	}, nil
}

func thresholdForRatio(scores []float64, ratio float64) float64 {
	sorted := make([]float64, len(scores))
	copy(sorted, scores)
	sort.Sort(sort.Reverse(sort.Float64Slice(sorted)))
	n := len(sorted)
	best, bestDiff := math.Nextafter(sorted[0], math.Inf(1)), ratio
	for i := range sorted {
		if i+1 < n && sorted[i+1] == sorted[i] {
			continue
		}
		if diff := math.Abs(float64(i+1)/float64(n) - ratio); diff < bestDiff {
			best, bestDiff = sorted[i], diff
		}
	}
	return best
}

func ratioAbove(scores []float64, t float64) float64 {
	n := 0
	for _, s := range scores {
		if s >= t {
			n++
		}
	}
	return float64(n) / float64(len(scores))
}
