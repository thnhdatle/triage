// SPDX-License-Identifier: Apache-2.0

package telemetry

// Histogram buckets are declared once.
var (
	// latencyBuckets covers sub-millisecond router work up to long upstream calls, in seconds.
	latencyBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}
	// scoreBuckets covers a difficulty score in [0, 1].
	scoreBuckets = []float64{0.05, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 1}
)

// maxLabelValues caps the distinct values of a label taken from configuration
// or user data.
const maxLabelValues = 50

const otherLabel = "other"
