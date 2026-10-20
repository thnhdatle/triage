// SPDX-License-Identifier: Apache-2.0

// Package fakestrategy provides a scriptable difficulty.Strategy for tests: a
// fixed score, a function of the request, a fixed error, or a strategy that
// blocks until its context is done. Every variant counts its calls.
//
//	s := fakestrategy.Fixed("bert", 0.9)
//	score, _ := s.Score(ctx, req) // score.P == 0.9, s.Calls() == 1
package fakestrategy

import (
	"context"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

// Func computes the outcome of one Score call.
type Func func(ctx context.Context, req chat.Request) (difficulty.Score, error)

// Strategy implements difficulty.Strategy and counts calls. It is safe for
// concurrent use.
type Strategy struct {
	name string
	fn   Func

	entered chan struct{}
	once    sync.Once

	mu    sync.Mutex
	calls int
}

// New returns a Strategy named name whose Score delegates to fn.
func New(name string, fn Func) *Strategy {
	return &Strategy{name: name, fn: fn, entered: make(chan struct{})}
}

// Fixed returns a Strategy that always scores p.
func Fixed(name string, p float64) *Strategy {
	return New(name, func(context.Context, chat.Request) (difficulty.Score, error) {
		return difficulty.Score{P: p}, nil
	})
}

// Failing returns a Strategy that always returns err.
func Failing(name string, err error) *Strategy {
	return New(name, func(context.Context, chat.Request) (difficulty.Score, error) {
		return difficulty.Score{}, err
	})
}

// Blocking returns a Strategy whose Score blocks until the context is done and
// then returns the context's error.
func Blocking(name string) *Strategy {
	return New(name, func(ctx context.Context, _ chat.Request) (difficulty.Score, error) {
		<-ctx.Done()
		return difficulty.Score{}, ctx.Err()
	})
}

// Name implements difficulty.Strategy.
func (s *Strategy) Name() string { return s.name }

// Score implements difficulty.Strategy.
func (s *Strategy) Score(ctx context.Context, req chat.Request) (difficulty.Score, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.entered) })
	return s.fn(ctx, req)
}

// Calls returns the number of Score calls started so far.
func (s *Strategy) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Entered returns a channel closed when the first Score call has started,
// letting a test wait for a Blocking strategy to be inside Score.
func (s *Strategy) Entered() <-chan struct{} { return s.entered }
