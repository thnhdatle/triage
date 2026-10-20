// SPDX-License-Identifier: Apache-2.0

package clock

import (
	"sort"
	"sync"
	"time"
)

// Fake is a manually advanced clock for tests. It is safe for concurrent use.
type Fake struct {
	mu      sync.Mutex // protects now, waiters
	now     time.Time
	waiters []*waiter
}

type waiter struct {
	at     time.Time
	ch     chan time.Time
	period time.Duration // > 0 for tickers
}

// NewFake returns a fake clock set to start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that receives when the clock is advanced past d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &waiter{at: f.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		w.ch <- f.now
		return w.ch
	}
	f.waiters = append(f.waiters, w)
	return w.ch
}

// NewTicker returns a ticker driven by Advance. Ticks that the receiver has
// not consumed are dropped, like time.Ticker.
func (f *Fake) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive ticker interval")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &waiter{at: f.now.Add(d), ch: make(chan time.Time, 1), period: d}
	f.waiters = append(f.waiters, w)
	return &fakeTicker{f: f, w: w}
}

// Waiters returns the number of pending After channels and active tickers,
// so tests can wait until code under test is blocked on the clock.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// Advance moves the clock forward by d, firing due timers in time order.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	end := f.now.Add(d)
	for {
		sort.SliceStable(f.waiters, func(i, j int) bool { return f.waiters[i].at.Before(f.waiters[j].at) })
		if len(f.waiters) == 0 || f.waiters[0].at.After(end) {
			break
		}
		w := f.waiters[0]
		f.now = w.at
		select {
		case w.ch <- w.at:
		default:
		}
		if w.period > 0 {
			w.at = w.at.Add(w.period)
			continue
		}
		f.waiters = f.waiters[1:]
	}
	f.now = end
}

type fakeTicker struct {
	f *Fake
	w *waiter
}

func (t *fakeTicker) C() <-chan time.Time { return t.w.ch }

func (t *fakeTicker) Stop() {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	for i, w := range t.f.waiters {
		if w == t.w {
			t.f.waiters = append(t.f.waiters[:i], t.f.waiters[i+1:]...)
			return
		}
	}
}
