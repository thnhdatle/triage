// SPDX-License-Identifier: Apache-2.0

// Package mf implements the RouteLLM matrix-factorization difficulty
// strategy and the deriver of its vector artifact.
//
// Because the model pair is fixed (gpt-4-1106-preview vs
// mixtral-8x7b-instruct-v0.1), the checkpoint folds into one vector u
// ([ComputeU], pure Go over a pure-Go safetensors parser), and scoring is one
// embedding of the scored text e (text-embedding-3-small, 1536 dimensions,
// truncated to an F3 estimate of 8,000 tokens) and p_strong = sigmoid(u . e).
// The embedding call sends the scored text off the machine: the egress policy
// (opt-in, detectors always run) is enforced by configuration and the route
// stage.
//
// Scoring cost (F9): [UsageEmbedder] adapts a provider's embeddings call and
// records its reported input tokens on the Score in flight; with the cache
// wired above it, Score.EmbeddingInputTokens is the upstream usage on a
// miss and 0 on a hit.
//
// Usage:
//
//	ue, err := mf.NewUsageEmbedder(openaiClient)
//	cache, err := embedcache.New(ue, cfg.CacheSize)
//	u, err := mf.LoadVector(vectorPath) // written by mf.Deriver at pull time
//	s, err := mf.New(cache, u)
//	score, err := s.Score(ctx, req)
package mf
