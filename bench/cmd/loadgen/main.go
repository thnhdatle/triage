// SPDX-License-Identifier: Apache-2.0

// Command loadgen sends a JSON body to an HTTP endpoint at a constant arrival
// rate (open model) and prints a JSON summary: sent, ok, errors, dropped,
// throughput and latency percentiles measured from each intended start.
//
//	loadgen -url http://127.0.0.1:8080/v1/chat/completions -body req.json -rate 500 -duration 60s
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/thnhdatle/triage/bench/loadgen"
)

type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ",") }

func (h *headerFlags) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("header %q: want Name: value", v)
	}
	*h = append(*h, strings.TrimSpace(name), strings.TrimSpace(value))
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

type output struct {
	URL        string  `json:"url"`
	Rate       float64 `json:"rate"`
	DurationS  float64 `json:"duration_s"`
	Sent       int     `json:"sent"`
	OK         int     `json:"ok"`
	Errors     int     `json:"errors"`
	Dropped    int     `json:"dropped"`
	Throughput float64 `json:"throughput_rps"`
	P50Ms      float64 `json:"p50_ms"`
	P99Ms      float64 `json:"p99_ms"`
	MaxMs      float64 `json:"max_ms"`
	MaxLagMs   float64 `json:"max_dispatch_lag_ms"`
	FirstError string  `json:"first_error,omitempty"`
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	url := fs.String("url", "", "endpoint to POST to")
	bodyPath := fs.String("body", "", "file holding the JSON request body ('-' for stdin)")
	rate := fs.Float64("rate", 100, "arrival rate, requests per second")
	duration := fs.Duration("duration", 10*time.Second, "schedule length")
	conns := fs.Int("conns", 1024, "maximum keep-alive connections")
	timeout := fs.Duration("timeout", 30*time.Second, "per-request timeout")
	maxInFlight := fs.Int("max-in-flight", 10_000, "outstanding request bound (excess is dropped)")
	var headers headerFlags
	fs.Var(&headers, "H", "extra request header 'Name: value' (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("flags: %w", err)
	}
	if *url == "" || *bodyPath == "" {
		return errors.New("-url and -body are required")
	}
	var (
		body []byte
		err  error
	)
	if *bodyPath == "-" {
		body, err = io.ReadAll(os.Stdin)
	} else {
		body, err = os.ReadFile(*bodyPath)
	}
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	hc := loadgen.NewClient(*conns, *timeout)
	cfg := loadgen.Config{Rate: *rate, Duration: *duration, MaxInFlight: *maxInFlight}
	res, err := loadgen.Run(ctx, cfg, func(ctx context.Context, _ int) error {
		return loadgen.Post(ctx, hc, *url, body, headers...)
	})
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	s := res.Summarize()
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output{
		URL: *url, Rate: *rate, DurationS: duration.Seconds(),
		Sent: s.Sent, OK: s.OK, Errors: s.Errors, Dropped: s.Dropped, Throughput: s.Throughput,
		P50Ms: loadgen.Ms(s.P50), P99Ms: loadgen.Ms(s.P99), MaxMs: loadgen.Ms(s.Max),
		MaxLagMs: loadgen.Ms(res.MaxLag), FirstError: s.FirstError,
	}); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}
