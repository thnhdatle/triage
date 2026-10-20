// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"context"
	"fmt"
	"sync"
)

// PoolConfig bounds concurrent runs on a shared Session (
// bert.max_concurrency and bert.queue_size).
type PoolConfig struct {
	// MaxConcurrency is the number of runs in flight at once; at least 1.
	MaxConcurrency int
	// QueueSize is the number of callers that may wait for a run slot; 0
	// rejects whenever every slot is busy.
	QueueSize int
}

// PoolStats is a snapshot of a pool's occupancy.
type PoolStats struct {
	InFlight int
	Queued   int
}

// Pool bounds concurrent use of one Session: at most MaxConcurrency leases
// are held at once and at most QueueSize callers wait for one. When the queue
// is full, Acquire fails at once with ErrQueueFull (reject).
// ORT sessions are safe for concurrent Run, so every lease shares the same
// session. The pool does not own the session; close the pool first, then the
// session.
type Pool struct {
	sess  *Session
	slots chan struct{} // a buffered send holds a run slot
	queue chan struct{} // a buffered send holds a wait-queue place
	done  chan struct{} // closed by Close
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

// Acquire returns a lease on a run slot, waiting in the bounded queue if
// every slot is busy. It fails with ErrQueueFull when the queue is full, with
// ErrPoolClosed after Close, and with the context's error (wrapped) when ctx
// ends while waiting. The lease must be released.
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

// lease wraps a slot already taken; a slot won concurrently with Close is
// given back.
func (p *Pool) lease() (*Lease, error) {
	select {
	case <-p.done:
		<-p.slots
		return nil, ErrPoolClosed
	default:
	}
	return &Lease{p: p}, nil
}

// Run acquires a lease, runs the session and releases the lease.
func (p *Pool) Run(ctx context.Context, inputs ...Tensor) ([]Tensor, error) {
	l, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer l.Release()
	return l.Run(ctx, inputs...)
}

// Stats returns the current number of held leases and queued callers.
func (p *Pool) Stats() PoolStats {
	return PoolStats{InFlight: len(p.slots), Queued: len(p.queue)}
}

// Close stops new acquisitions, wakes queued callers with ErrPoolClosed and
// waits until every lease is released or ctx ends. It is safe to call more
// than once; the session stays open.
func (p *Pool) Close(ctx context.Context) error {
	p.once.Do(func() { close(p.done) })
	taken := 0
	// Give back the slots Close took, so a repeated Close does not block.
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

// Lease is a held run slot of a Pool.
type Lease struct {
	p    *Pool
	once sync.Once
}

// Session returns the pooled session.
func (l *Lease) Session() *Session { return l.p.sess }

// Run runs the pooled session (see Session.Run).
func (l *Lease) Run(ctx context.Context, inputs ...Tensor) ([]Tensor, error) {
	return l.p.sess.Run(ctx, inputs...)
}

// Release returns the slot; later calls do nothing.
func (l *Lease) Release() {
	l.once.Do(func() { <-l.p.slots })
}
