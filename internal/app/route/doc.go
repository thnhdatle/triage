// SPDX-License-Identifier: Apache-2.0

// Package route runs the routing pipeline and records one Decision per
// request.
//
// Main types: Pipeline (Chat, ChatStream, Embeddings), Route and Options
// (immutable, built by config), Deps, Outcome, the consumer ports
// Dispatcher, Embedder, Health and Observer, and the errors with Code.
//
// Stage order for chat requests:
//
//  1. resolve the route by the model field (ErrUnknownRoute, 404);
//  2. effective privacy mode: the stricter of the route mode and
//     X-Triage-Privacy; a looser or equal value is ignored, a stricter one
//     beyond max_client_mode or an unknown value is privacy.ErrModeNotAllowed;
//  3. privacy scan when the mode is not off, or the route uses mf (under off
//     findings then only steer mf and never constrain routing, and oversize
//     never rejects); oversize with reject is privacy.ErrScanLimit;
//  4. localities: local only when local_only has findings or mask is
//     constrained (oversize, ner_unavailable); a detector error is handled
//     fail-closed as local only (reasons privacy:scan_error,
//     privacy:local_only; under off it only makes mf fall back). mask with
//     findings builds the request's privacy.Mapping (privacy:mask);
//  5. rules, first match wins: X-Triage-Tier (when allowed), then
//     min_prompt_tokens (selection token counter over PromptText) and
//     last_user_regex in configured order (rule:header|length|regex);
//  6. difficulty when no rule matched: strategy none uses default_tier
//     (default:<tier>); mf with findings, oversize or a failed scan uses
//     difficulty.mf.fallback (strategy:mf->bert(privacy)); Score runs under
//     the per-strategy timeout, driven by the injected clock; an error,
//     timeout, rejection or invalid score selects difficulty.on_error
//     (strategy:<name> error-><tier>(<cause>));
//  7. selection: pool targets of the tier that are chat targets, healthy, in
//     the allowed localities, with a context window >= prompt + requested
//     output tokens and the required capabilities, ordered by selection
//     mode; an empty tier applies on_tier_unavailable (tier:escalate,
//     tier:degrade) without relaxing localities, else
//     privacy.ErrNoLocalTarget (local only) or ErrNoEligibleTarget;
//  8. chain: the tier's candidates, then the fallback entries under the same
//     filter, deduplicated; ContextLengthPool ignores only the window filter;
//  9. dispatch (one in-flight count on the primary target for
//     least_inflight), then fallback:<status> reasons per failed attempt;
//  10. the decision is recorded exactly once, also on errors, with counts of
//     findings by type but never prompt text or finding values.
//
// Usage:
//
//	p, err := route.New(route.Deps{
//		Targets: pool, Routes: routes, Strategies: strategies, Detectors: detectors,
//		Dispatcher: dispatcher, Recorder: recorder, Clock: clock.Real(), NewID: newID,
//	}, route.Options{})
//	out, err := p.Chat(ctx, req)
//	code, status := route.Code(err) // OpenAI error code and HTTP status
//	w.Header().Set("X-Triage-Decision", out.DecisionID)
package route
