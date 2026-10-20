// SPDX-License-Identifier: Apache-2.0

// Package health tracks target health: passive reports from
// dispatch and an active probe loop.
//
// Tracker implements route.Health (IsHealthy) and dispatch.Health (Report).
// health.failure_threshold consecutive failures, from attempts or probes,
// make a target unhealthy for health.cooldown; a success resets the count but
// never shortens a running cooldown. Run probes every health.interval with
// the Checks returned by its source (one check may cover several targets that
// share an upstream, e.g. GET /v1/models of one provider), so a reload changes
// the probed set without restarting the loop.
//
//	tr, err := health.New(clock.Real(), health.Options{FailureThreshold: 3, Cooldown: 30 * time.Second})
//	go tr.Run(ctx, func() []health.Check { return checks })
//	ok := tr.IsHealthy("local-small")
package health
