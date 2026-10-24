// SPDX-License-Identifier: Apache-2.0

package eval

import (
	"math"
	"math/rand/v2"
	"sort"
)

// Bootstrap returns a 95 percent confidence interval for stat.
func Bootstrap(n, resamples int, seed int64, stat func(idx []int) float64) (lo, hi float64) {
	if n <= 0 || resamples <= 0 {
		return math.NaN(), math.NaN()
	}
	//nolint:gosec // G404,G115: seeded, reproducible resampling; seed bits are reinterpreted on purpose.
	rng := rand.New(rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15))
	idx := make([]int, n)
	stats := make([]float64, 0, resamples)
	for range resamples {
		for i := range idx {
			idx[i] = rng.IntN(n)
		}
		if v := stat(idx); !math.IsNaN(v) {
			stats = append(stats, v)
		}
	}
	if len(stats) == 0 {
		return math.NaN(), math.NaN()
	}
	sort.Float64s(stats)
	m := len(stats)
	loI := int(math.Floor(0.025 * float64(m)))
	hiI := min(m-1, int(math.Ceil(0.975*float64(m)))-1)
	return stats[loI], stats[max(hiI, loI)]
}
