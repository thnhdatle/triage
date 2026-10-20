// SPDX-License-Identifier: Apache-2.0

package route

import (
	"time"

	"github.com/thnhdatle/triage/internal/domain/model"
)

// EventKind identifies a pipeline event for metrics and tracing.
type EventKind string

// Event kinds and the Event fields each one sets.
const (
	// EventFindings: Route, Counts (findings by type, never values).
	EventFindings EventKind = "findings"
	// EventOversize: Route.
	EventOversize EventKind = "oversize"
	// EventTier: Route, Tier (the selected tier).
	EventTier EventKind = "tier"
	// EventScoring: Strategy, Duration (every Score call, failed ones too).
	EventScoring EventKind = "scoring"
	// EventScoringRejected: Strategy, Reason ("timeout" or "rejected").
	EventScoringRejected EventKind = "scoring_rejected"
	// EventMFExternalEmbedding: Route; mf scored with an external embedding
	// call (Score.EmbeddingInputTokens > 0, i.e. not a cache hit).
	EventMFExternalEmbedding EventKind = "mf_external_embedding"
	// EventUnmaskFailure: Route; the response was only partially unmasked.
	EventUnmaskFailure EventKind = "unmask_failure"
	// EventRouteOverhead: Route, Duration from request start to dispatch.
	EventRouteOverhead EventKind = "route_overhead"
)

// Event is one observation of the pipeline.
type Event struct {
	Kind     EventKind
	Route    string
	Strategy string
	Tier     model.Tier
	Reason   string
	Duration time.Duration
	// Counts is a copy the observer may keep.
	Counts map[string]int
}

// Observer receives pipeline events (the wire package connects Prometheus and OpenTelemetry). Observe
// is called on the request goroutine; implementations must be safe for
// concurrent use and must not block.
type Observer interface {
	Observe(e Event)
}

// NopObserver discards every event.
type NopObserver struct{}

// Observe implements Observer.
func (NopObserver) Observe(Event) {}
