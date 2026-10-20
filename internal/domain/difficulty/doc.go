// SPDX-License-Identifier: Apache-2.0

// Package difficulty defines the Strategy and Embedder ports, Score, Threshold
// and scored-text extraction.
//
//	text, err := difficulty.ScoredText(req) // last user message, original values
//	s, err := strategy.Score(ctx, req)
//	tier := threshold.Tier(s.P)
package difficulty
