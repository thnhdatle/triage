// SPDX-License-Identifier: Apache-2.0

// Package anthropic implements the provider adapter that translates to and
// from the Anthropic Messages API.
//
// Client implements provider.Provider. Complete and Stream translate the
// neutral chat.Request into POST {base}/messages: system messages become the
// top-level system prompt, tool messages become tool_result blocks in a user
// turn, consecutive turns of the same role are merged, tools and tool_choice
// are mapped, stop becomes stop_sequences, and max_tokens is always sent
// (WithDefaultMaxTokens when the request sets none). Responses and stream
// events (message_start, content_block_start/delta, message_delta,
// message_stop) are translated into the neutral response and chunk model;
// usage counts cache reads and cache writes as prompt tokens and cache reads
// as cached prompt tokens. Embed always fails with provider.ErrBadRequest.
//
// Upstream failures are normalised to *provider.Error: 429 is ErrRateLimited
// with Retry-After, 408/504 and deadlines are ErrTimeout, a prompt-too-long
// 400 is ErrContextLength, other 4xx are ErrBadRequest, 5xx (incl. 529
// overloaded), transport failures, malformed bodies and streams that end
// without message_stop are ErrUpstreamUnavailable. A cancelled context is
// returned as its cause. Error messages carry the upstream status and error
// type only, never upstream message text.
//
//	c, err := anthropic.New(anthropic.WithAPIKey(key))
//	if err != nil {
//		return err
//	}
//	resp, err := c.Complete(ctx, req)
package anthropic
