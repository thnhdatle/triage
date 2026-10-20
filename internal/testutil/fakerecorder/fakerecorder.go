// SPDX-License-Identifier: Apache-2.0

// Package fakerecorder is an in-memory decision.Recorder for tests.
//
//	rec := fakerecorder.New()
//	rec.Record(decision.Decision{ID: "r1"})
//	_ = rec.Len() // 1
package fakerecorder

import (
	"sync"

	"github.com/thnhdatle/triage/internal/domain/decision"
)

// Recorder stores every recorded decision. It is safe for concurrent use.
type Recorder struct {
	mu        sync.Mutex
	decisions []decision.Decision
}

// New returns an empty Recorder.
func New() *Recorder { return &Recorder{} }

// Record implements decision.Recorder.
func (r *Recorder) Record(d decision.Decision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, d)
}

// Decisions returns a copy of the recorded decisions in record order.
func (r *Recorder) Decisions() []decision.Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]decision.Decision(nil), r.decisions...)
}

// Len returns the number of recorded decisions.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.decisions)
}
