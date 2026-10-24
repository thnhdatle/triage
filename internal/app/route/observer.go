// SPDX-License-Identifier: Apache-2.0

package route

import (
	"time"

	"github.com/thnhdatle/triage/internal/domain/model"
)

type EventKind string

const (
	EventFindings            EventKind = "findings"
	EventOversize            EventKind = "oversize"
	EventTier                EventKind = "tier"
	EventScoring             EventKind = "scoring"
	EventScoringRejected     EventKind = "scoring_rejected"
	EventMFExternalEmbedding EventKind = "mf_external_embedding"
	EventUnmaskFailure       EventKind = "unmask_failure"
	EventRouteOverhead       EventKind = "route_overhead"
)

type Event struct {
	Kind     EventKind
	Route    string
	Strategy string
	Tier     model.Tier
	Reason   string
	Duration time.Duration
	Counts   map[string]int
}

type Observer interface {
	Observe(e Event)
}

type NopObserver struct{}

func (NopObserver) Observe(Event) {}
