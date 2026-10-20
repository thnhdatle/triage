// SPDX-License-Identifier: Apache-2.0

package decisionlog_test

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/decisionlog"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

func newLog(t *testing.T, size int) *decisionlog.Log {
	t.Helper()
	l, err := decisionlog.New(size)
	require.NoError(t, err)
	return l
}

func ids(p decisionlog.Page) []string {
	out := make([]string, 0, len(p.Items))
	for _, d := range p.Items {
		out = append(out, d.ID)
	}
	return out
}

func TestNewRejectsBadSize(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, -1} {
		_, err := decisionlog.New(size)
		require.ErrorIs(t, err, decisionlog.ErrInvalidSize)
	}
}

func TestRecordGetAndEviction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		size    int
		n       int
		wantLen int
		gone    []string
		present []string
	}{
		{name: "under capacity", size: 5, n: 3, wantLen: 3, present: []string{"d1", "d2", "d3"}},
		{name: "exact capacity", size: 3, n: 3, wantLen: 3, present: []string{"d1", "d3"}},
		{name: "wraps", size: 3, n: 5, wantLen: 3, gone: []string{"d1", "d2"}, present: []string{"d3", "d4", "d5"}},
		{name: "size one", size: 1, n: 4, wantLen: 1, gone: []string{"d1", "d3"}, present: []string{"d4"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := newLog(t, tc.size)
			for i := 1; i <= tc.n; i++ {
				l.Record(decision.Decision{ID: fmt.Sprintf("d%d", i)})
			}
			assert.Equal(t, tc.wantLen, l.Len())
			for _, id := range tc.gone {
				_, ok := l.Get(id)
				assert.False(t, ok, id)
			}
			for _, id := range tc.present {
				d, ok := l.Get(id)
				require.True(t, ok, id)
				assert.Equal(t, id, d.ID)
			}
		})
	}
}

func TestGetUnknown(t *testing.T) {
	t.Parallel()
	l := newLog(t, 2)
	_, ok := l.Get("nope")
	assert.False(t, ok)
}

func TestDuplicateIDLatestWinsAndEvictionKeepsIndex(t *testing.T) {
	t.Parallel()
	l := newLog(t, 2)
	l.Record(decision.Decision{ID: "x", Route: "old"})
	l.Record(decision.Decision{ID: "x", Route: "new"})
	l.Record(decision.Decision{ID: "y"}) // evicts the first x only
	d, ok := l.Get("x")
	require.True(t, ok)
	assert.Equal(t, "new", d.Route)
	l.Record(decision.Decision{ID: "z"}) // evicts the second x
	_, ok = l.Get("x")
	assert.False(t, ok)
}

func TestStoresDeepCopies(t *testing.T) {
	t.Parallel()
	l := newLog(t, 2)
	score := 0.5
	d := decision.Decision{
		ID: "a", Findings: map[string]int{"email": 1}, Reasons: []string{"r"},
		Attempts: []decision.Attempt{{Target: "t"}}, Score: &score, Threshold: &score,
	}
	l.Record(d)
	d.Findings["email"] = 9
	d.Reasons[0] = "mutated"
	d.Attempts[0].Target = "mutated"
	score = 0.9

	got, ok := l.Get("a")
	require.True(t, ok)
	assert.Equal(t, 1, got.Findings["email"])
	assert.Equal(t, "r", got.Reasons[0])
	assert.Equal(t, "t", got.Attempts[0].Target)
	assert.InDelta(t, 0.5, *got.Score, 1e-9)
	assert.InDelta(t, 0.5, *got.Threshold, 1e-9)

	got.Findings["email"] = 7
	got.Reasons[0] = "x"
	again, _ := l.Get("a")
	assert.Equal(t, 1, again.Findings["email"])
	assert.Equal(t, "r", again.Reasons[0])
}

func fill(t *testing.T, size int) *decisionlog.Log {
	t.Helper()
	l := newLog(t, size)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	specs := []decision.Decision{
		{
			ID: "1", Route: "a", Strategy: "bert", Tier: model.TierWeak, Privacy: privacy.ModeMask,
			Attempts: []decision.Attempt{{Target: "small"}},
		},
		{
			ID: "2", Route: "b", Strategy: "length", Tier: model.TierStrong, Privacy: privacy.ModeOff,
			Attempts: []decision.Attempt{{Target: "small"}, {Target: "big"}}, Error: "upstream_unavailable",
		},
		{
			ID: "3", Route: "a", Strategy: "bert", Tier: model.TierStrong, Privacy: privacy.ModeLocalOnly,
			Attempts: []decision.Attempt{{Target: "big"}},
		},
		{ID: "4", Route: "a", Strategy: "none", Tier: model.TierWeak, Privacy: privacy.ModeMask, Error: "x"},
	}
	for i, d := range specs {
		d.Time = base.Add(time.Duration(i) * time.Hour)
		l.Record(d)
	}
	return l
}

func TestListFilters(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		f    decisionlog.Filter
		want []string
	}{
		{name: "none", want: []string{"4", "3", "2", "1"}},
		{name: "route", f: decisionlog.Filter{Route: "a"}, want: []string{"4", "3", "1"}},
		{name: "strategy", f: decisionlog.Filter{Strategy: "bert"}, want: []string{"3", "1"}},
		{name: "tier", f: decisionlog.Filter{Tier: model.TierStrong}, want: []string{"3", "2"}},
		{name: "privacy", f: decisionlog.Filter{Privacy: privacy.ModeMask}, want: []string{"4", "1"}},
		{name: "target any attempt", f: decisionlog.Filter{Target: "big"}, want: []string{"3", "2"}},
		{name: "only errors", f: decisionlog.Filter{OnlyErrors: true}, want: []string{"4", "2"}},
		{name: "since inclusive", f: decisionlog.Filter{Since: base.Add(2 * time.Hour)}, want: []string{"4", "3"}},
		{name: "until exclusive", f: decisionlog.Filter{Until: base.Add(2 * time.Hour)}, want: []string{"2", "1"}},
		{name: "combined", f: decisionlog.Filter{Route: "a", Tier: model.TierWeak, OnlyErrors: true}, want: []string{"4"}},
		{name: "no match", f: decisionlog.Filter{Route: "zzz"}, want: []string{}},
	}
	l := fill(t, 10)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := l.List(tc.f, 0, "")
			require.NoError(t, err)
			assert.Equal(t, tc.want, ids(p))
			assert.Empty(t, p.NextCursor)
			assert.NotNil(t, p.Items)
		})
	}
}

func TestListPaging(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		f     decisionlog.Filter
		limit int
		pages [][]string
	}{
		{name: "limit 2", limit: 2, pages: [][]string{{"4", "3"}, {"2", "1"}}},
		{name: "limit 1", limit: 1, pages: [][]string{{"4"}, {"3"}, {"2"}, {"1"}}},
		{name: "limit exceeds", limit: 50, pages: [][]string{{"4", "3", "2", "1"}}},
		{name: "filtered", f: decisionlog.Filter{Route: "a"}, limit: 2, pages: [][]string{{"4", "3"}, {"1"}}},
		{name: "default limit", limit: -1, pages: [][]string{{"4", "3", "2", "1"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := fill(t, 10)
			cursor := ""
			for i, want := range tc.pages {
				p, err := l.List(tc.f, tc.limit, cursor)
				require.NoError(t, err)
				assert.Equal(t, want, ids(p), "page %d", i)
				cursor = p.NextCursor
				if i == len(tc.pages)-1 {
					assert.Empty(t, cursor)
				} else {
					assert.NotEmpty(t, cursor)
				}
			}
		})
	}
}

func TestListLimitCapped(t *testing.T) {
	t.Parallel()
	l := newLog(t, decisionlog.MaxLimit+50)
	for i := range decisionlog.MaxLimit + 50 {
		l.Record(decision.Decision{ID: strconv.Itoa(i)})
	}
	p, err := l.List(decisionlog.Filter{}, decisionlog.MaxLimit*10, "")
	require.NoError(t, err)
	assert.Len(t, p.Items, decisionlog.MaxLimit)
	assert.NotEmpty(t, p.NextCursor)
}

func TestListCursorErrors(t *testing.T) {
	t.Parallel()
	l := fill(t, 10)
	for _, c := range []string{"abc", "0", "-1", "99"} {
		_, err := l.List(decisionlog.Filter{}, 1, c)
		require.ErrorIs(t, err, decisionlog.ErrInvalidCursor, c)
	}
}

func TestListAfterWrapAndEvictionBetweenPages(t *testing.T) {
	t.Parallel()
	l := newLog(t, 3)
	for i := 1; i <= 5; i++ {
		l.Record(decision.Decision{ID: strconv.Itoa(i)})
	}
	p, err := l.List(decisionlog.Filter{}, 2, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"5", "4"}, ids(p))

	// Evict 3 while the client holds a cursor; the next page is empty, not an error.
	l.Record(decision.Decision{ID: "6"})
	l.Record(decision.Decision{ID: "7"})
	p2, err := l.List(decisionlog.Filter{}, 2, p.NextCursor)
	require.NoError(t, err)
	assert.Empty(t, p2.Items)
	assert.Empty(t, p2.NextCursor)
}

func TestListEmptyLog(t *testing.T) {
	t.Parallel()
	l := newLog(t, 3)
	p, err := l.List(decisionlog.Filter{}, 10, "")
	require.NoError(t, err)
	assert.Empty(t, p.Items)
	assert.Empty(t, p.NextCursor)
}

func TestConcurrentUse(t *testing.T) {
	t.Parallel()
	l := newLog(t, 64)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 500 {
				l.Record(decision.Decision{ID: fmt.Sprintf("%d-%d", w, i), Findings: map[string]int{"a": i}})
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				p, err := l.List(decisionlog.Filter{}, 10, "")
				assert.NoError(t, err)
				for _, d := range p.Items {
					if got, ok := l.Get(d.ID); ok {
						assert.Equal(t, d.ID, got.ID)
					}
				}
				_ = l.Len()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 64, l.Len())
}
