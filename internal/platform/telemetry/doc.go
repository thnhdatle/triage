// SPDX-License-Identifier: Apache-2.0

// Package telemetry implements Prometheus metrics, OpenTelemetry
// tracing (OTLP, off by default), a route.Observer, an HTTP middleware and a
// /metrics handler.
//
//	t, err := telemetry.Setup(ctx, telemetry.Options{ServiceName: "triage", Version: "1.0.0"})
//	defer t.Shutdown(ctx)
//	srv, _ := httpapi.New(r, c, id, httpapi.WithMiddleware(t.Middleware()), httpapi.WithMetrics(t.MetricsHandler()))
//
// No label or attribute ever carries prompt content or personal data: labels
// are route, strategy, tier, provider, model, reason, finding type and HTTP
// status, and values taken from configuration or user data are capped to a
// fixed number of distinct values plus "other".
package telemetry
