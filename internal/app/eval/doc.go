// SPDX-License-Identifier: Apache-2.0

// Package eval implements the pure computation of the evaluation harness: answer scorers,
// APGR, bootstrap confidence intervals and threshold helpers. The live and labels runners build on it.
//
// # APGR (RouteLLM paper, arXiv 2406.18665, equations 6-8)
//
// For N items with a router score (higher means "send to strong"), weak correctness w_i and strong
// correctness s_i:
//
//  1. Sort the items by score descending. Items with equal scores form a tie group and are routed
//     together: routing j of the m items of a group to strong is valued at the expected gain of a
//     random j-subset, that is j/m times the group's summed (s_i - w_i). Accuracy is therefore
//     piecewise linear inside a tie group and exact at group boundaries.
//  2. acc(k) = (sum of w_i + gain of the top k) / N for k = 0..N strong calls, so acc(0) = acc_weak and
//     acc(N) = acc_strong.
//  3. PGR(k) = (acc(k) - acc_weak) / (acc_strong - acc_weak). PGR(0) = 0, PGR(N) = 1. When
//     acc_strong == acc_weak the metric is undefined: APGR returns ErrNoGap and Curve reports NaN.
//  4. APGR = the area under PGR over the strong-call fraction k/N in [0,1], by the trapezoid rule over
//     the N+1 points (equation 7). The paper approximates the integral on ten call percentages
//     (equation 8) without naming the grid, so the exact trapezoid over every k is used: it is the
//     continuous quantity that the ten-point mean estimates, it needs no per-grid threshold search and
//     a random router scores 0.5 in expectation.
//
// A perfect oracle (score = s_i - w_i) maximises the curve pointwise.
//
// # Usage
//
//	items := []eval.Item{
//		{Score: 0.9, WeakCorrect: false, StrongCorrect: true},
//		{Score: 0.1, WeakCorrect: true, StrongCorrect: true},
//	}
//	apgr, err := eval.APGR(items)
//	lo, hi := eval.Bootstrap(len(items), 10000, 1, func(idx []int) float64 { return 0 })
//	score := eval.ScoreGSM8K("... so the total is $1,234.50", "#### 1234.5") // true
package eval
