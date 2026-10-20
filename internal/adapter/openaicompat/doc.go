// SPDX-License-Identifier: Apache-2.0

// Package openaicompat implements the provider adapter for OpenAI-compatible
// backends (OpenAI, LM Studio, vLLM, fakellm).
//
// Client implements provider.Provider: Complete and Stream call
// POST {base}/chat/completions, Embed calls POST {base}/embeddings and Models
// calls GET {base}/models. Client.Embedder returns the difficulty.Embedder view
// used by the mf strategy.
//
// Upstream failures are normalised to *provider.Error (see errors.go for the
// table): 429 is ErrRateLimited with Retry-After, 408/504 and deadlines are
// ErrTimeout, a context-length 400 is ErrContextLength, other 4xx are
// ErrBadRequest, 5xx, transport failures, malformed bodies and streams that
// end without [DONE] are ErrUpstreamUnavailable. A cancelled context is
// returned as the context's error. Error messages carry the upstream status
// and error type/code only, never upstream message text, because upstreams may
// echo request content.
//
// Timeouts: non-streaming calls are bounded by WithTimeout (default
// DefaultTimeout) and the caller's context; a stream is bounded by the same
// timeout until its response headers arrive, then only by the caller's
// context (dispatch enforces the per-chunk attempt timeout).
//
//	c, err := openaicompat.New("http://localhost:1234/v1", openaicompat.WithAPIKey(key))
//	if err != nil {
//		return err
//	}
//	resp, err := c.Complete(ctx, req)
package openaicompat
