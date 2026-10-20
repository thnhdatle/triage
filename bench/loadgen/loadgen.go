// SPDX-License-Identifier: Apache-2.0

// Package loadgen is an open-model (constant arrival rate) load generator.
// Request i is due at start + i/rate whether or not earlier
// requests have completed, and its latency is measured from that intended
// start, so a stalled server shows up as latency instead of being hidden by a
// closed loop waiting for it (coordinated omission).
package loadgen

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Config is one constant-rate run.
type Config struct {
	// Rate is the arrival rate in requests per second.
	Rate float64
	// Duration is the length of the schedule; Rate*Duration requests are sent.
	Duration time.Duration
	// MaxInFlight bounds outstanding requests; a request due while the bound
	// is reached is not sent and is recorded as dropped. 0 means 10,000.
	MaxInFlight int
}

// Requests is the number of requests on the schedule.
func (c Config) Requests() int { return int(math.Round(c.Rate * c.Duration.Seconds())) }

// Offset is the intended start of request i relative to the start of the run.
func (c Config) Offset(i int) time.Duration {
	return time.Duration(float64(i) / c.Rate * float64(time.Second))
}

func (c Config) validate() error {
	if c.Rate <= 0 || math.IsNaN(c.Rate) || math.IsInf(c.Rate, 0) {
		return fmt.Errorf("loadgen: rate must be positive, got %v", c.Rate)
	}
	if c.Duration <= 0 {
		return fmt.Errorf("loadgen: duration must be positive, got %v", c.Duration)
	}
	if c.Requests() < 1 {
		return errors.New("loadgen: schedule has no requests")
	}
	return nil
}

// Do sends request i of the schedule. It must be safe for concurrent use.
type Do func(ctx context.Context, i int) error

// Sample is the outcome of one scheduled request.
type Sample struct {
	// Latency is completion time minus intended start.
	Latency time.Duration
	// Err is the request error; nil means success.
	Err error
	// Dropped is set when the request was not sent because MaxInFlight was reached.
	Dropped bool
}

// Result is a completed run. Samples is indexed by request number.
type Result struct {
	Config  Config
	Samples []Sample
	// Elapsed is the time from the start of the run to the last completion.
	Elapsed time.Duration
	// MaxLag is the largest delay between a request's intended start and the
	// moment the dispatcher launched it (a generator health indicator).
	MaxLag time.Duration
}

// Run executes the schedule and waits for every request to finish. Cancelling
// ctx stops dispatching; requests not yet due are recorded with ctx's error.
func Run(ctx context.Context, cfg Config, do Do) (Result, error) {
	if err := cfg.validate(); err != nil {
		return Result{}, err
	}
	r := &runner{cfg: cfg, do: do, limit: int64(cfg.MaxInFlight), res: Result{Config: cfg}}
	if r.limit <= 0 {
		r.limit = 10_000
	}
	n := cfg.Requests()
	r.res.Samples = make([]Sample, n)
	r.start = time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for i := 0; i < n; {
		if err := ctx.Err(); err != nil {
			for ; i < n; i++ {
				r.res.Samples[i] = Sample{Err: err, Dropped: true}
			}
			break
		}
		i = r.launchDue(ctx, i, n)
		if i < n {
			timer.Reset(cfg.Offset(i) - time.Since(r.start))
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
		}
	}
	r.wg.Wait()
	r.res.Elapsed = time.Duration(r.lastEnd.Load())
	return r.res, nil
}

// runner is the state of one Run.
type runner struct {
	cfg      Config
	do       Do
	limit    int64
	start    time.Time
	res      Result
	wg       sync.WaitGroup
	inFlight atomic.Int64
	lastEnd  atomic.Int64
}

// launchDue starts every request from i on that is due and returns the index
// of the first request that is not.
func (r *runner) launchDue(ctx context.Context, i, n int) int {
	now := time.Since(r.start)
	for ; i < n && r.cfg.Offset(i) <= now; i++ {
		due := r.cfg.Offset(i)
		r.res.MaxLag = max(r.res.MaxLag, now-due)
		if r.inFlight.Load() >= r.limit {
			r.res.Samples[i] = Sample{Dropped: true, Err: errors.New("loadgen: max in-flight reached")}
			continue
		}
		r.inFlight.Add(1)
		r.wg.Add(1)
		go r.send(ctx, i, due)
	}
	return i
}

func (r *runner) send(ctx context.Context, i int, due time.Duration) {
	defer r.wg.Done()
	defer r.inFlight.Add(-1)
	err := r.do(ctx, i)
	end := time.Since(r.start)
	r.res.Samples[i] = Sample{Latency: end - due, Err: err}
	for {
		cur := r.lastEnd.Load()
		if int64(end) <= cur || r.lastEnd.CompareAndSwap(cur, int64(end)) {
			return
		}
	}
}

// Summary is the aggregate of a run.
type Summary struct {
	Sent, OK, Errors, Dropped int
	// Throughput is successful requests per second over Elapsed.
	Throughput    float64
	P50, P99, Max time.Duration
	FirstError    string
}

// Summarize aggregates the successful samples of r.
func (r Result) Summarize() Summary {
	var s Summary
	lat := make([]time.Duration, 0, len(r.Samples))
	for _, x := range r.Samples {
		switch {
		case x.Dropped:
			s.Dropped++
		case x.Err != nil:
			s.Sent++
			s.Errors++
			if s.FirstError == "" {
				s.FirstError = x.Err.Error()
			}
		default:
			s.Sent++
			s.OK++
			lat = append(lat, x.Latency)
		}
	}
	if r.Elapsed > 0 {
		s.Throughput = float64(s.OK) / r.Elapsed.Seconds()
	}
	s.P50, s.P99, s.Max = Percentile(lat, 0.50), Percentile(lat, 0.99), Percentile(lat, 1)
	return s
}

// Percentile returns the q-quantile (0 <= q <= 1) of ds by the nearest-rank
// method; 0 for an empty slice. ds is not modified.
func Percentile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	rank := int(math.Ceil(q*float64(len(s)))) - 1
	return s[min(max(rank, 0), len(s)-1)]
}

// Ms converts a duration to fractional milliseconds.
func Ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
