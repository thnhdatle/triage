// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/thnhdatle/triage/internal/app/health"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const closeTimeout = 30 * time.Second

type shared struct {
	value any
	close func(ctx context.Context) error
	refs  atomic.Int64
}

type generation struct {
	cfg        *config.Config
	plan       *plan
	pipeline   *route.Pipeline
	targets    model.Pool
	routes     []string
	checks     []health.Check
	strategies map[string]difficulty.Strategy
	detectors  []privacy.Detector
	counter    chat.TokenCounter
	resources  map[string]*shared

	refs   atomic.Int64
	onIdle atomic.Pointer[func()]
}

func newGeneration() *generation {
	g := &generation{resources: map[string]*shared{}, strategies: map[string]difficulty.Strategy{}}
	g.refs.Store(1)
	return g
}

func (g *generation) tryAcquire() bool {
	for {
		n := g.refs.Load()
		if n == 0 {
			return false
		}
		if g.refs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (g *generation) release() {
	if g.refs.Add(-1) == 0 {
		if fn := g.onIdle.Load(); fn != nil {
			(*fn)()
		}
	}
}

func (g *generation) dispose(ctx context.Context) []error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	var errs []error
	for _, r := range g.resources {
		if r.refs.Add(-1) != 0 || r.close == nil {
			continue
		}
		if err := r.close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

type live struct {
	cur atomic.Pointer[generation]
}

func (l *live) acquire() *generation {
	for {
		g := l.cur.Load()
		if g == nil || g.tryAcquire() {
			return g
		}
	}
}

func (l *live) swap(g *generation) *generation { return l.cur.Swap(g) }
