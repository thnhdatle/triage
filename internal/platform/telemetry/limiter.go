// SPDX-License-Identifier: Apache-2.0

package telemetry

import "sync"

// limiter maps label values to themselves until max distinct values were seen,
// then to "other".
type limiter struct {
	mu   sync.Mutex
	seen map[string]struct{}
	max  int
}

func newLimiter() *limiter {
	return &limiter{seen: make(map[string]struct{}), max: maxLabelValues}
}

func (l *limiter) get(v string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[v]; ok {
		return v
	}
	if len(l.seen) >= l.max {
		return otherLabel
	}
	l.seen[v] = struct{}{}
	return v
}
