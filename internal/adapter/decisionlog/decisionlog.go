// SPDX-License-Identifier: Apache-2.0

package decisionlog

import (
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const DefaultSize = 10000

const DefaultLimit = 100

const MaxLimit = 1000

type entry struct {
	seq uint64
	d   decision.Decision
}

// Log is a fixed-size ring buffer of decisions.
type Log struct {
	mu    sync.RWMutex
	slots []entry
	total uint64
	byID  map[string]uint64
}

var _ decision.Recorder = (*Log)(nil)

// New returns a Log that keeps the newest size decisions.
func New(size int) (*Log, error) {
	if size <= 0 {
		return nil, ErrInvalidSize
	}
	return &Log{slots: make([]entry, size), byID: make(map[string]uint64)}, nil
}

// Record stores a decision.
func (l *Log) Record(d decision.Decision) {
	d = clone(d)
	l.mu.Lock()
	defer l.mu.Unlock()
	slot := &l.slots[l.total%uint64(len(l.slots))]
	if slot.seq != 0 && l.byID[slot.d.ID] == slot.seq {
		delete(l.byID, slot.d.ID)
	}
	l.total++
	*slot = entry{seq: l.total, d: d}
	l.byID[d.ID] = l.total
}

func (l *Log) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.slots) - int(uint64(len(l.slots))-l.lenLocked()) //nolint:gosec // bounded by len(slots)
}

func (l *Log) lenLocked() uint64 { return min(l.total, uint64(len(l.slots))) }

// Get returns the decision with the given id.
func (l *Log) Get(id string) (decision.Decision, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	seq, ok := l.byID[id]
	if !ok {
		return decision.Decision{}, false
	}
	return clone(l.slots[(seq-1)%uint64(len(l.slots))].d), true
}

type Filter struct {
	Route      string
	Strategy   string
	Tier       model.Tier
	Privacy    privacy.Mode
	Target     string
	OnlyErrors bool
	Since      time.Time
	Until      time.Time
}

func (f Filter) Match(d decision.Decision) bool {
	switch {
	case f.Route != "" && d.Route != f.Route,
		f.Strategy != "" && d.Strategy != f.Strategy,
		f.Tier != model.TierNone && d.Tier != f.Tier,
		f.Privacy != "" && d.Privacy != f.Privacy,
		f.OnlyErrors && d.Error == "",
		!f.Since.IsZero() && d.Time.Before(f.Since),
		!f.Until.IsZero() && !d.Time.Before(f.Until):
		return false
	}
	if f.Target == "" {
		return true
	}
	return slices.ContainsFunc(d.Attempts, func(a decision.Attempt) bool { return a.Target == f.Target })
}

type Page struct {
	Items      []decision.Decision
	NextCursor string
}

// List returns a page of decisions, newest first.
func (l *Log) List(f Filter, limit int, cursor string) (Page, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	l.mu.RLock()
	defer l.mu.RUnlock()
	next := l.total
	if cursor != "" {
		before, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil || before == 0 || before > l.total+1 {
			return Page{}, ErrInvalidCursor
		}
		next = before - 1
	}
	oldest := l.total - l.lenLocked() + 1
	page := Page{Items: []decision.Decision{}}
	var last uint64
	for seq := next; seq >= oldest && seq > 0; seq-- {
		e := l.slots[(seq-1)%uint64(len(l.slots))]
		if !f.Match(e.d) {
			continue
		}
		if len(page.Items) == limit {
			page.NextCursor = strconv.FormatUint(last, 10)
			return page, nil
		}
		page.Items = append(page.Items, clone(e.d))
		last = seq
	}
	return page, nil
}

func clone(d decision.Decision) decision.Decision {
	d.Findings = maps.Clone(d.Findings)
	d.Reasons = slices.Clone(d.Reasons)
	d.Attempts = slices.Clone(d.Attempts)
	if d.Score != nil {
		v := *d.Score
		d.Score = &v
	}
	if d.Threshold != nil {
		v := *d.Threshold
		d.Threshold = &v
	}
	return d
}
