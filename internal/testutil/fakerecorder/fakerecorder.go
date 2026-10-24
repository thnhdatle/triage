// SPDX-License-Identifier: Apache-2.0

// Package fakerecorder is an in-memory decision.Recorder for tests.
package fakerecorder

import (
	"sync"

	"github.com/thnhdatle/triage/internal/domain/decision"
)

// Recorder stores the recorded decisions.
type Recorder struct {
	mu        sync.Mutex
	decisions []decision.Decision
}

// New returns an empty Recorder.
func New() *Recorder { return &Recorder{} }

func (r *Recorder) Record(d decision.Decision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, d)
}

func (r *Recorder) Decisions() []decision.Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]decision.Decision(nil), r.decisions...)
}

func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.decisions)
}
