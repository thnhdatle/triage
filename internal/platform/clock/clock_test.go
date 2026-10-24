// SPDX-License-Identifier: Apache-2.0

package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/platform/clock"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestFake_After_FiresOnAdvance(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch)
	ch := f.After(time.Second)
	assert.Equal(t, 1, f.Waiters())
	f.Advance(500 * time.Millisecond)
	select {
	case <-ch:
		t.Fatal("fired early")
	default:
	}
	f.Advance(500 * time.Millisecond)
	require.Equal(t, epoch.Add(time.Second), <-ch)
	assert.Equal(t, 0, f.Waiters())
	assert.Equal(t, epoch.Add(time.Second), f.Now())
}

func TestFake_After_NonPositiveFiresImmediately(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch)
	assert.Equal(t, epoch, <-f.After(0))
}

func TestFake_NewTicker_TicksAndStops(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch)
	tk := f.NewTicker(time.Second)
	f.Advance(time.Second)
	assert.Equal(t, epoch.Add(time.Second), <-tk.C())
	f.Advance(3 * time.Second)
	assert.Equal(t, epoch.Add(2*time.Second), <-tk.C())
	tk.Stop()
	assert.Equal(t, 0, f.Waiters())
	tk.Stop()
	assert.Panics(t, func() { f.NewTicker(0) })
}

func TestReal_Basics(t *testing.T) {
	t.Parallel()
	c := clock.Real()
	assert.False(t, c.Now().IsZero())
	<-c.After(time.Millisecond)
	tk := c.NewTicker(time.Millisecond)
	<-tk.C()
	tk.Stop()
}
