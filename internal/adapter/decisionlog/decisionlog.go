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

// DefaultSize is the default buffer size (decisions.buffer_size).
const DefaultSize = 10000

// DefaultLimit is the page size List uses when the limit is not positive.
const DefaultLimit = 100

// MaxLimit caps the page size of List.
const MaxLimit = 1000

type entry struct {
	seq uint64
	d   decision.Decision
}

// Log is a fixed-size ring buffer of decisions. It is safe for concurrent use.
type Log struct {
	mu    sync.RWMutex
	slots []entry
	total uint64 // number of decisions ever recorded; newest seq
	byID  map[string]uint64
}

var _ decision.Recorder = (*Log)(nil)

// New returns a Log holding at most size decisions.
func New(size int) (*Log, error) {
	if size <= 0 {
		return nil, ErrInvalidSize
	}
	return &Log{slots: make([]entry, size), byID: make(map[string]uint64)}, nil
}

// Record implements decision.Recorder. It stores a deep copy and evicts the
// oldest decision when the buffer is full. It never blocks on I/O.
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

// Len returns the number of decisions currently held.
func (l *Log) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.slots) - int(uint64(len(l.slots))-l.lenLocked()) //nolint:gosec // bounded by len(slots)
}

func (l *Log) lenLocked() uint64 { return min(l.total, uint64(len(l.slots))) }

// Get returns the decision with the id. When an id was recorded more than
// once, the latest one wins.
func (l *Log) Get(id string) (decision.Decision, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	seq, ok := l.byID[id]
	if !ok {
		return decision.Decision{}, false
	}
	return clone(l.slots[(seq-1)%uint64(len(l.slots))].d), true
}

// Filter selects decisions for List. Zero fields match everything.
type Filter struct {
	Route    string
	Strategy string
	Tier     model.Tier
	Privacy  privacy.Mode
	// Target matches decisions with at least one attempt on this target.
	Target string
	// OnlyErrors keeps decisions whose Error is set.
	OnlyErrors bool
	// Since keeps decisions at or after this time; Until keeps those before it.
	Since time.Time
	Until time.Time
}

// Match reports whether d satisfies the filter.
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

// Page is one page of List results, newest first.
type Page struct {
	Items []decision.Decision
	// NextCursor is empty on the last page.
	NextCursor string
}

// List returns up to limit decisions matching f, newest first, starting after
// the cursor from a previous Page (empty for the first page). A limit <= 0
// means DefaultLimit and is capped at MaxLimit. Decisions evicted between
// pages are skipped; pages never repeat a decision.
func (l *Log) List(f Filter, limit int, cursor string) (Page, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	l.mu.RLock()
	defer l.mu.RUnlock()
	next := l.total // seq of the newest candidate
	if cursor != "" {
		before, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil || before == 0 || before > l.total+1 {
			return Page{}, ErrInvalidCursor
		}
		next = before - 1
	}
	oldest := l.total - l.lenLocked() + 1
	page := Page{Items: []decision.Decision{}}
	var last uint64 // seq of the last item on the page
	for seq := next; seq >= oldest && seq > 0; seq-- {
		e := l.slots[(seq-1)%uint64(len(l.slots))]
		if !f.Match(e.d) {
			continue
		}
		if len(page.Items) == limit {
			// A further match exists: the cursor resumes before the last item.
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
