// SPDX-License-Identifier: Apache-2.0

package health_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/app/health"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var errDown = errors.New("down")

func TestNew_NilClock(t *testing.T) {
	t.Parallel()
	_, err := health.New(nil, health.Options{})
	require.Error(t, err)
}

func TestTracker_Report_ThresholdAndCooldown(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	var changes []bool
	tr, err := health.New(clk, health.Options{FailureThreshold: 2, Cooldown: time.Minute},
		health.WithOnChange(func(_ string, h bool) { changes = append(changes, h) }))
	require.NoError(t, err)

	assert.True(t, tr.IsHealthy("a"), "unknown targets are healthy")
	tr.Report("a", errDown)
	assert.True(t, tr.IsHealthy("a"))
	tr.Report("a", nil) // success resets the count
	tr.Report("a", errDown)
	assert.True(t, tr.IsHealthy("a"))
	tr.Report("a", errDown)
	assert.False(t, tr.IsHealthy("a"))
	tr.Report("a", nil) // success does not end the cooldown
	assert.False(t, tr.IsHealthy("a"))
	clk.Advance(time.Minute)
	assert.True(t, tr.IsHealthy("a"))
	assert.True(t, tr.IsHealthy("b"))
	assert.Equal(t, []bool{false}, changes)
}

func TestTracker_SetOptions(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	tr, err := health.New(clk, health.Options{})
	require.NoError(t, err)
	tr.SetOptions(health.Options{FailureThreshold: 1, Cooldown: time.Second})
	tr.Report("a", errDown)
	assert.False(t, tr.IsHealthy("a"))
	clk.Advance(time.Second)
	assert.True(t, tr.IsHealthy("a"))
}

func TestTracker_Run_ProbesAndStops(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	var mu sync.Mutex
	seen := map[string]bool{}
	tr, err := health.New(clk, health.Options{Interval: time.Second, FailureThreshold: 1, Cooldown: time.Hour},
		health.WithOnChange(func(n string, h bool) {
			mu.Lock()
			seen[n] = h
			mu.Unlock()
		}))
	require.NoError(t, err)
	var probes atomic.Int64
	checks := []health.Check{
		{Targets: []string{"up"}, Probe: func(context.Context) error { probes.Add(1); return nil }},
		{Targets: []string{"down1", "down2"}, Probe: func(context.Context) error { probes.Add(1); return errDown }},
		{Targets: []string{"skipped"}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx, func() []health.Check { return checks }) }()

	testutil.Eventually(t, func() bool { return probes.Load() >= 2 && clk.Waiters() > 0 }, 5*time.Second)
	assert.True(t, tr.IsHealthy("up"))
	assert.False(t, tr.IsHealthy("down1"))
	assert.False(t, tr.IsHealthy("down2"))
	clk.Advance(time.Second)
	testutil.Eventually(t, func() bool { return probes.Load() >= 4 }, 5*time.Second)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, map[string]bool{"up": true, "down1": false, "down2": false, "skipped": true}, seen)
}

func TestTracker_Run_CancelledProbeNotCounted(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	tr, err := health.New(clk, health.Options{FailureThreshold: 1})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	checks := []health.Check{{Targets: []string{"x"}, Probe: func(c context.Context) error {
		close(started)
		<-c.Done()
		return c.Err()
	}}}
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx, func() []health.Check { return checks }) }()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	assert.True(t, tr.IsHealthy("x"))
}
