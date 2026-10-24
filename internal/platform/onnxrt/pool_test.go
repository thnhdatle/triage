// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/testutil"
)

func newTestPool(t *testing.T, qs int) *Pool {
	t.Helper()
	p, err := NewPool(&Session{}, PoolConfig{MaxConcurrency: 1, QueueSize: qs})
	require.NoError(t, err)
	return p
}

func acquireAsync(ctx context.Context, p *Pool) <-chan error {
	ch := make(chan error, 1)
	go func() {
		l, err := p.Acquire(ctx)
		if err == nil {
			l.Release()
		}
		ch <- err
	}()
	return ch
}

func TestNewPoolValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    *Session
		cfg  PoolConfig
	}{
		{name: "nil session", cfg: PoolConfig{MaxConcurrency: 1}},
		{name: "zero concurrency", s: &Session{}, cfg: PoolConfig{}},
		{name: "negative queue", s: &Session{}, cfg: PoolConfig{MaxConcurrency: 1, QueueSize: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewPool(tc.s, tc.cfg)
			assert.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestPoolAcquireRelease(t *testing.T) {
	t.Parallel()
	s := &Session{}
	p, err := NewPool(s, PoolConfig{MaxConcurrency: 2})
	require.NoError(t, err)
	a, err := p.Acquire(t.Context())
	require.NoError(t, err)
	b, err := p.Acquire(t.Context())
	require.NoError(t, err)
	assert.Same(t, s, a.Session())
	assert.Equal(t, PoolStats{InFlight: 2}, p.Stats())

	_, err = p.Acquire(t.Context())
	require.ErrorIs(t, err, ErrQueueFull, "queue size 0 rejects when every slot is busy")

	a.Release()
	a.Release()
	assert.Equal(t, PoolStats{InFlight: 1}, p.Stats())
	b.Release()
	require.NoError(t, p.Close(t.Context()))
}

func TestPoolQueue(t *testing.T) {
	t.Parallel()
	p := newTestPool(t, 1)
	held, err := p.Acquire(t.Context())
	require.NoError(t, err)

	waiter := acquireAsync(t.Context(), p)
	testutil.Eventually(t, func() bool { return p.Stats().Queued == 1 }, 5*time.Second)

	_, err = p.Acquire(t.Context())
	require.ErrorIs(t, err, ErrQueueFull)
	var rej interface{ Rejected() bool }
	require.ErrorAs(t, err, &rej)

	held.Release()
	require.NoError(t, <-waiter, "the queued caller gets the released slot")
	assert.Equal(t, PoolStats{}, p.Stats())
	require.NoError(t, p.Close(t.Context()))
}

func TestPoolAcquireContext(t *testing.T) {
	t.Parallel()
	p := newTestPool(t, 4)

	done, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := p.Acquire(done)
	require.ErrorIs(t, err, context.Canceled, "an ended context fails before taking a slot")
	_, err = p.Run(done)
	require.ErrorIs(t, err, context.Canceled)

	held, err := p.Acquire(t.Context())
	require.NoError(t, err)
	ctx, cancelWait := context.WithCancel(t.Context())
	waiter := acquireAsync(ctx, p)
	testutil.Eventually(t, func() bool { return p.Stats().Queued == 1 }, 5*time.Second)
	cancelWait()
	require.ErrorIs(t, <-waiter, context.Canceled)
	assert.Equal(t, 0, p.Stats().Queued, "a cancelled waiter leaves the queue")
	held.Release()
	require.NoError(t, p.Close(t.Context()))
}

func TestPoolClose(t *testing.T) {
	t.Parallel()
	p := newTestPool(t, 2)
	held, err := p.Acquire(t.Context())
	require.NoError(t, err)
	waiter := acquireAsync(t.Context(), p)
	testutil.Eventually(t, func() bool { return p.Stats().Queued == 1 }, 5*time.Second)

	short, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, p.Close(short), context.Canceled, "Close waits for held leases until ctx ends")
	require.ErrorIs(t, <-waiter, ErrPoolClosed, "Close wakes queued callers")
	_, err = p.Acquire(t.Context())
	require.ErrorIs(t, err, ErrPoolClosed)

	closed := make(chan error, 1)
	go func() { closed <- p.Close(t.Context()) }()
	held.Release()
	require.NoError(t, <-closed, "Close returns once the last lease is released")
	require.NoError(t, p.Close(t.Context()), "Close is idempotent")
}

func TestPoolLeaseRaceWithClose(t *testing.T) {
	t.Parallel()
	p := newTestPool(t, 0)
	p.slots <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, p.Close(ctx), context.Canceled, "Close cannot finish while the slot is held")
	_, err := p.lease()
	require.ErrorIs(t, err, ErrPoolClosed)
	assert.Equal(t, 0, p.Stats().InFlight, "the slot is given back")
}
