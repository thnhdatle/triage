// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"context"
	"fmt"
	"sync"
)

type PoolConfig struct {
	MaxConcurrency int
	QueueSize      int
}

type PoolStats struct {
	InFlight int
	Queued   int
}

// Pool limits concurrent runs of one Session and queues the callers that wait.
type Pool struct {
	sess  *Session
	slots chan struct{}
	queue chan struct{}
	done  chan struct{}
	once  sync.Once
}

// NewPool returns a pool over s.
func NewPool(s *Session, cfg PoolConfig) (*Pool, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil session", ErrInvalidConfig)
	}
	if cfg.MaxConcurrency < 1 || cfg.QueueSize < 0 {
		return nil, fmt.Errorf("%w: max concurrency %d (min 1), queue size %d (min 0)",
			ErrInvalidConfig, cfg.MaxConcurrency, cfg.QueueSize)
	}
	return &Pool{
		sess:  s,
		slots: make(chan struct{}, cfg.MaxConcurrency),
		queue: make(chan struct{}, cfg.QueueSize),
		done:  make(chan struct{}),
	}, nil
}

// Acquire returns a lease on a run slot, waiting in the queue if all slots are busy.
func (p *Pool) Acquire(ctx context.Context) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("onnxrt: acquire: %w", err)
	}
	select {
	case <-p.done:
		return nil, ErrPoolClosed
	default:
	}
	select {
	case p.slots <- struct{}{}:
		return p.lease()
	default:
	}
	select {
	case p.queue <- struct{}{}:
	default:
		return nil, ErrQueueFull
	}
	defer func() { <-p.queue }()
	select {
	case p.slots <- struct{}{}:
		return p.lease()
	case <-p.done:
		return nil, ErrPoolClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("onnxrt: acquire: %w", ctx.Err())
	}
}

func (p *Pool) lease() (*Lease, error) {
	select {
	case <-p.done:
		<-p.slots
		return nil, ErrPoolClosed
	default:
	}
	return &Lease{p: p}, nil
}

func (p *Pool) Run(ctx context.Context, inputs ...Tensor) ([]Tensor, error) {
	l, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer l.Release()
	return l.Run(ctx, inputs...)
}

func (p *Pool) Stats() PoolStats {
	return PoolStats{InFlight: len(p.slots), Queued: len(p.queue)}
}

func (p *Pool) Close(ctx context.Context) error {
	p.once.Do(func() { close(p.done) })
	taken := 0
	defer func() {
		for range taken {
			<-p.slots
		}
	}()
	for taken < cap(p.slots) {
		select {
		case p.slots <- struct{}{}:
			taken++
		case <-ctx.Done():
			return fmt.Errorf("onnxrt: close pool: %w", ctx.Err())
		}
	}
	return nil
}

type Lease struct {
	p    *Pool
	once sync.Once
}

func (l *Lease) Session() *Session { return l.p.sess }

func (l *Lease) Run(ctx context.Context, inputs ...Tensor) ([]Tensor, error) {
	return l.p.sess.Run(ctx, inputs...)
}

func (l *Lease) Release() {
	l.once.Do(func() { <-l.p.slots })
}
