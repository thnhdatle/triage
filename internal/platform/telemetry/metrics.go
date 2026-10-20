// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/thnhdatle/triage/internal/app/route"
)

// metrics holds every collector of the process, registered on one registry.
type metrics struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInflight prometheus.Gauge

	overhead      *prometheus.HistogramVec
	upstream      *prometheus.HistogramVec
	score         *prometheus.HistogramVec
	scoringDur    *prometheus.HistogramVec
	queueDepth    *prometheus.GaugeVec
	scoringRej    *prometheus.CounterVec
	tiers         *prometheus.CounterVec
	findings      *prometheus.CounterVec
	oversize      *prometheus.CounterVec
	unmaskFail    *prometheus.CounterVec
	mfExternal    *prometheus.CounterVec
	fallbacks     *prometheus.CounterVec
	targetFlight  *prometheus.GaugeVec
	targetHealthy *prometheus.GaugeVec
	reloads       *prometheus.CounterVec
	cost          *prometheus.CounterVec

	routes, strategies, providers, models, types, targets *limiter
}

func newMetrics(version, commit, goVersion string) *metrics {
	reg := prometheus.NewRegistry()
	f := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	}
	h := func(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
	}
	g := func(name, help string, labels ...string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	}
	m := &metrics{
		reg: reg,
		httpRequests: f("triage_http_requests_total", "HTTP requests by method, route pattern and status.",
			"method", "path", "status"),
		httpDuration: h("triage_http_request_duration_seconds", "HTTP request duration.", latencyBuckets, "method", "path"),
		httpInflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "triage_http_requests_inflight", Help: "HTTP requests in flight.",
		}),

		overhead: h("triage_route_overhead_seconds", "Router overhead from request start to dispatch.",
			latencyBuckets, "route"),
		upstream: h("triage_upstream_request_duration_seconds", "Upstream provider request duration.",
			latencyBuckets, "provider", "model", "status"),
		score:      h("triage_strategy_score", "Difficulty score by strategy.", scoreBuckets, "strategy"),
		scoringDur: h("triage_scoring_duration_seconds", "Scoring duration by strategy.", latencyBuckets, "strategy"),
		queueDepth: g("triage_scoring_queue_depth", "Scoring queue depth by strategy.", "strategy"),
		scoringRej: f("triage_scoring_rejected_total", "Rejected scoring calls.", "strategy", "reason"),
		tiers:      f("triage_tier_decisions_total", "Tier decisions.", "route", "tier"),
		findings:   f("triage_privacy_findings_total", "Privacy findings by type.", "route", "type"),
		oversize:   f("triage_privacy_oversize_total", "Requests too large for privacy scanning.", "route"),
		unmaskFail: f("triage_unmask_failures_total", "Responses only partially unmasked.", "route"),
		mfExternal: f("triage_mf_external_embeddings_total", "mf scorings needing an external embedding call.",
			"route"),
		fallbacks:     f("triage_fallback_attempts_total", "Fallback attempts.", "route", "reason"),
		targetFlight:  g("triage_target_inflight", "Requests in flight per target.", "target"),
		targetHealthy: g("triage_target_healthy", "Target health, 1 healthy and 0 unhealthy.", "target"),
		reloads:       f("triage_config_reload_total", "Configuration reloads.", "result"),
		cost:          f("triage_cost_usd_total", "Cost in USD by route and target.", "route", "target"),

		routes:     newLimiter(),
		strategies: newLimiter(),
		providers:  newLimiter(),
		models:     newLimiter(),
		types:      newLimiter(),
		targets:    newLimiter(),
	}
	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "triage_build_info", Help: "Build information."},
		[]string{"version", "commit", "go_version"})
	info.WithLabelValues(version, commit, goVersion).Set(1)
	reg.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), info,
		m.httpRequests, m.httpDuration, m.httpInflight, m.overhead, m.upstream, m.score, m.scoringDur, m.queueDepth,
		m.scoringRej, m.tiers, m.findings, m.oversize, m.unmaskFail, m.mfExternal, m.fallbacks,
		m.targetFlight, m.targetHealthy, m.reloads, m.cost,
	)
	return m
}

// Observer returns the route.Observer that records pipeline events.
func (t *Telemetry) Observer() route.Observer { return observer{t.m} }

type observer struct{ m *metrics }

// Observe implements route.Observer.
func (o observer) Observe(e route.Event) {
	m := o.m
	switch e.Kind {
	case route.EventFindings:
		r := m.routes.get(e.Route)
		for typ, n := range e.Counts {
			if n > 0 {
				m.findings.WithLabelValues(r, m.types.get(typ)).Add(float64(n))
			}
		}
	case route.EventOversize:
		m.oversize.WithLabelValues(m.routes.get(e.Route)).Inc()
	case route.EventTier:
		m.tiers.WithLabelValues(m.routes.get(e.Route), tierLabel(string(e.Tier))).Inc()
	case route.EventScoring:
		m.scoringDur.WithLabelValues(m.strategies.get(e.Strategy)).Observe(e.Duration.Seconds())
	case route.EventScoringRejected:
		m.scoringRej.WithLabelValues(m.strategies.get(e.Strategy), reasonLabel(e.Reason)).Inc()
	case route.EventMFExternalEmbedding:
		m.mfExternal.WithLabelValues(m.routes.get(e.Route)).Inc()
	case route.EventUnmaskFailure:
		m.unmaskFail.WithLabelValues(m.routes.get(e.Route)).Inc()
	case route.EventRouteOverhead:
		m.overhead.WithLabelValues(m.routes.get(e.Route)).Observe(e.Duration.Seconds())
	}
}

// tierLabel and reasonLabel bound label values that come from a closed set.
func tierLabel(v string) string {
	switch v {
	case "weak", "strong":
		return v
	}
	return otherLabel
}

func reasonLabel(v string) string {
	switch v {
	case "timeout", "rejected":
		return v
	}
	return otherLabel
}

// ObserveScore records a strategy's difficulty score in [0, 1].
func (t *Telemetry) ObserveScore(strategy string, score float64) {
	t.m.score.WithLabelValues(t.m.strategies.get(strategy)).Observe(score)
}

// SetScoringQueueDepth sets the number of waiting scoring calls of a strategy.
func (t *Telemetry) SetScoringQueueDepth(strategy string, depth int) {
	t.m.queueDepth.WithLabelValues(t.m.strategies.get(strategy)).Set(float64(depth))
}

// ObserveUpstream records one upstream request. status is a short outcome
// such as "ok", "error" or an HTTP status code, never a message.
func (t *Telemetry) ObserveUpstream(provider, modelName, status string, d time.Duration) {
	t.m.upstream.WithLabelValues(t.m.providers.get(provider), t.m.models.get(modelName), t.statusLabel(status)).
		Observe(d.Seconds())
}

func (t *Telemetry) statusLabel(s string) string {
	if s == "" {
		return "unknown"
	}
	if len(s) > 16 {
		return otherLabel
	}
	return s
}

// ObserveFallback counts a fallback attempt; reason is an error code.
func (t *Telemetry) ObserveFallback(routeName, reason string) {
	t.m.fallbacks.WithLabelValues(t.m.routes.get(routeName), t.statusLabel(reason)).Inc()
}

// AddTargetInflight adds delta (+1 or -1) to a target's in-flight gauge.
func (t *Telemetry) AddTargetInflight(target string, delta int) {
	t.m.targetFlight.WithLabelValues(t.m.targets.get(target)).Add(float64(delta))
}

// SetTargetHealth records whether a target is healthy.
func (t *Telemetry) SetTargetHealth(target string, healthy bool) {
	v := 0.0
	if healthy {
		v = 1
	}
	t.m.targetHealthy.WithLabelValues(t.m.targets.get(target)).Set(v)
}

// ObserveConfigReload counts a reload; result is "success" or "error"
// ("failure" is accepted as a synonym of "error").
func (t *Telemetry) ObserveConfigReload(result string) {
	switch result {
	case "success", "error":
	case "failure":
		result = "error"
	default:
		result = otherLabel
	}
	t.m.reloads.WithLabelValues(result).Inc()
}

// ObserveCost adds usd to triage_cost_usd_total{route,target}. Zero and
// negative amounts are ignored.
func (t *Telemetry) ObserveCost(routeName, target string, usd float64) {
	if !(usd > 0) {
		return
	}
	t.m.cost.WithLabelValues(t.m.routes.get(routeName), t.m.targets.get(target)).Add(usd)
}

// MetricsHandler serves the Prometheus exposition (httpapi.WithMetrics).
func (t *Telemetry) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(t.m.reg, promhttp.HandlerOpts{Registry: t.m.reg})
}

// Gatherer returns the registry, for tests and extra collectors.
func (t *Telemetry) Gatherer() prometheus.Gatherer { return t.m.reg }
