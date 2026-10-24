// SPDX-License-Identifier: Apache-2.0

// Package fakestrategy provides a scriptable fake difficulty.Strategy for tests.
package fakestrategy

import (
	"context"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

type Func func(ctx context.Context, req chat.Request) (difficulty.Score, error)

// Strategy is a fake difficulty.Strategy that counts calls.
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

func Fixed(name string, p float64) *Strategy {
	return New(name, func(context.Context, chat.Request) (difficulty.Score, error) {
		return difficulty.Score{P: p}, nil
	})
}

func Failing(name string, err error) *Strategy {
	return New(name, func(context.Context, chat.Request) (difficulty.Score, error) {
		return difficulty.Score{}, err
	})
}

func Blocking(name string) *Strategy {
	return New(name, func(ctx context.Context, _ chat.Request) (difficulty.Score, error) {
		<-ctx.Done()
		return difficulty.Score{}, ctx.Err()
	})
}

func (s *Strategy) Name() string { return s.name }

func (s *Strategy) Score(ctx context.Context, req chat.Request) (difficulty.Score, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.entered) })
	return s.fn(ctx, req)
}

func (s *Strategy) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *Strategy) Entered() <-chan struct{} { return s.entered }
