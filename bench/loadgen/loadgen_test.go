// SPDX-License-Identifier: Apache-2.0

package loadgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestRun_SendsEveryScheduledRequestOnce(t *testing.T) {
	t.Parallel()
	cfg := Config{Rate: 200, Duration: 250 * time.Millisecond}
	require.Equal(t, 50, cfg.Requests())
	seen := make([]atomic.Int32, cfg.Requests())
	res, err := Run(context.Background(), cfg, func(_ context.Context, i int) error {
		seen[i].Add(1)
		return nil
	})
	require.NoError(t, err)
	for i := range seen {
		assert.Equal(t, int32(1), seen[i].Load(), "request %d", i)
	}
	s := res.Summarize()
	assert.Equal(t, 50, s.OK)
	assert.Zero(t, s.Errors+s.Dropped)
	// The schedule spans (n-1)/rate; an open model cannot finish earlier.
	assert.GreaterOrEqual(t, res.Elapsed, cfg.Offset(cfg.Requests()-1))
}

func TestRun_OpenModelDoesNotWaitForSlowRequests(t *testing.T) {
	t.Parallel()
	// A closed loop with one worker would take 20 x 50 ms = 1 s; the open
	// model keeps the arrival schedule and finishes shortly after 100 ms + 50 ms.
	cfg := Config{Rate: 200, Duration: 100 * time.Millisecond}
	start := time.Now()
	res, err := Run(context.Background(), cfg, func(ctx context.Context, _ int) error {
		select {
		case <-time.After(50 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 600*time.Millisecond)
	s := res.Summarize()
	assert.Equal(t, 20, s.OK)
	assert.GreaterOrEqual(t, s.P50, 50*time.Millisecond, "latency is measured from the intended start")
}

func TestRun_DropsBeyondMaxInFlight(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	cfg := Config{Rate: 1000, Duration: 20 * time.Millisecond, MaxInFlight: 5}
	done := make(chan Result, 1)
	go func() {
		res, err := Run(context.Background(), cfg, func(context.Context, int) error {
			<-block
			return nil
		})
		assert.NoError(t, err)
		done <- res
	}()
	time.Sleep(100 * time.Millisecond)
	close(block)
	s := (<-done).Summarize()
	assert.Equal(t, 5, s.OK)
	assert.Equal(t, cfg.Requests()-5, s.Dropped)
}

func TestRun_RecordsErrors(t *testing.T) {
	t.Parallel()
	cfg := Config{Rate: 1000, Duration: 10 * time.Millisecond}
	res, err := Run(context.Background(), cfg, func(_ context.Context, i int) error {
		if i%2 == 0 {
			return errors.New("boom")
		}
		return nil
	})
	require.NoError(t, err)
	s := res.Summarize()
	assert.Equal(t, 5, s.Errors)
	assert.Equal(t, 5, s.OK)
	assert.Equal(t, 10, s.Sent)
	assert.Equal(t, "boom", s.FirstError)
}

func TestRun_CancelStopsDispatch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{Rate: 100, Duration: 10 * time.Second}
	var calls atomic.Int32
	time.AfterFunc(50*time.Millisecond, cancel)
	res, err := Run(ctx, cfg, func(context.Context, int) error { calls.Add(1); return nil })
	require.NoError(t, err)
	assert.Less(t, int(calls.Load()), 100)
	assert.Positive(t, res.Summarize().Dropped)
}

func TestRun_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	invalid := []Config{{Rate: 0, Duration: time.Second}, {Rate: 10, Duration: 0}, {Rate: 1, Duration: time.Millisecond}}
	for _, cfg := range invalid {
		_, err := Run(context.Background(), cfg, func(context.Context, int) error { return nil })
		assert.Error(t, err, "%+v", cfg)
	}
}

func TestPercentile_NearestRank(t *testing.T) {
	t.Parallel()
	ds := make([]time.Duration, 100)
	for i := range ds {
		ds[len(ds)-1-i] = time.Duration(i+1) * time.Millisecond
	}
	assert.Equal(t, 50*time.Millisecond, Percentile(ds, 0.5))
	assert.Equal(t, 99*time.Millisecond, Percentile(ds, 0.99))
	assert.Equal(t, 100*time.Millisecond, Percentile(ds, 1))
	assert.Equal(t, time.Millisecond, Percentile(ds, 0))
	assert.Equal(t, time.Millisecond, ds[len(ds)-1], "input is not reordered")
	assert.Zero(t, Percentile(nil, 0.99))
}

func TestPost_StatusAndHeaders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "1" || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	hc := NewClient(4, time.Second)
	defer hc.CloseIdleConnections()
	require.NoError(t, Post(context.Background(), hc, srv.URL, []byte(`{}`), "X-Test", "1"))
	err := Post(context.Background(), hc, srv.URL, []byte(`{}`))
	require.ErrorContains(t, err, "status 418")
}
