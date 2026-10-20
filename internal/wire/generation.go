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

// closeTimeout bounds closing the resources of a retired generation.
const closeTimeout = 30 * time.Second

// shared is a resource that generations may share across reloads: a provider
// client, a scoring strategy with an ORT session, the NER detector. refs
// counts the generations holding it; the last one to become idle closes it.
type shared struct {
	value any
	close func(ctx context.Context) error
	refs  atomic.Int64
}

// generation is everything built from one configuration: the pipeline and
// the resources it uses. Requests hold a reference for their whole lifetime,
// so a reload never closes a resource an in-flight request still uses
// (in-flight requests finish on the old configuration).
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

	// refs counts the live reference (1 while current) plus requests.
	refs atomic.Int64
	// onIdle runs once when refs drops to zero after retirement.
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

// dispose drops g's reference on each of its resources and closes those no
// other generation holds.
// ctx only carries values: closing must finish even after shutdown began.
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

// live holds the current generation.
type live struct {
	cur atomic.Pointer[generation]
}

// acquire returns the current generation with a reference held, or nil
// after shutdown. The caller must release it.
func (l *live) acquire() *generation {
	for {
		g := l.cur.Load()
		if g == nil || g.tryAcquire() {
			return g
		}
	}
}

// swap installs g and returns the previous generation (still holding its
// live reference, which the caller drops after arranging cleanup).
func (l *live) swap(g *generation) *generation { return l.cur.Swap(g) }
