// SPDX-License-Identifier: Apache-2.0

// Package provider defines the Provider port (Complete, Stream, Embed, Models)
// and the normalised provider errors adapters translate upstream failures into.
//
//	resp, err := p.Complete(ctx, req)
//	if errors.Is(err, provider.ErrRateLimited) {
//		wait := provider.RetryAfter(err)
//	}
package provider
