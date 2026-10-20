// SPDX-License-Identifier: Apache-2.0

// Package dispatch runs the fallback chain of a routed request: the
// retry budget, the per-attempt privacy transform by target locality (F5),
// unmasking, usage and cost per attempt (F9), passive health reports (F3) and
// the stream relay with continuation.
//
// Main types: Dispatcher (Complete, Stream), Plan (built by app/route),
// Result, Sink (implemented by the HTTP stream writer), Options and the
// typed failure *Error.
//
// Policy:
//   - Chain entries are tried in order and the chain cycles back to its start,
//     so a one-target chain still gets Options.MaxAttempts attempts on 429.
//   - Retryable errors are those of provider.IsRetryable (rate limit,
//     unavailable, timeout, 5xx). ErrBadRequest and ErrContextLength end the
//     dispatch, except that with ContextLengthFallback one ErrContextLength
//     is retried on the first ContextLengthPool target with a larger window.
//   - A target is retried (cycled to) only after a jittered exponential
//     backoff (uniform in [d/2, d], d = min(Max, Base*2^(n-1)) for its n-th
//     retry) and after its Retry-After; a target not tried yet is attempted
//     at once. A Retry-After above MaxRetryAfter removes that target from
//     the request instead of waiting.
//   - No attempt goes to a target outside Plan.Localities. Attempts to cloud
//     targets get Plan.Mapping.MaskRequest of their request, checked with
//     Mapping.Leaks before sending (a leak refuses every cloud target);
//     attempts to local targets get the original request.
//   - Streams: a failure before the first content byte is relayed is retried
//     transparently. After it, StreamFailureError emits Sink.Error;
//     StreamFailureContinue sends the next target the original messages, the
//     client-visible text so far and ContinuationInstruction (re-scanned with
//     Plan.Detectors and masked for cloud targets; a failed re-scan refuses
//     cloud targets) and relays its chunks into the same stream with the id
//     and model of the first relayed chunk. A cut inside a tool call or in a
//     multi-choice stream always errors out. Upstream usage is always
//     requested so cut and continued attempts are priced.
//   - Timeouts use the injected clock: AttemptTimeout (per target override)
//     for a whole Complete call, or until the first chunk and between chunks
//     of a stream; TotalTimeout for the request including waits.
//
// Usage:
//
//	d, err := dispatch.New(providers, clock.Real(), dispatch.Options{})
//	res, err := d.Complete(ctx, dispatch.Plan{
//		Request: req, Chain: chain, Localities: model.AllLocalities(), Mapping: m,
//	})
//	var de *dispatch.Error
//	if errors.As(err, &de) { /* de.Attempts go into the decision */ }
//	res, err = d.Stream(ctx, plan, sink) // then write [DONE] when err == nil
package dispatch
