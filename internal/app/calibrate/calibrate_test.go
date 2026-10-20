// SPDX-License-Identifier: Apache-2.0

package calibrate_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/calibrate"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

// fakeStrategy scores a request by the number in its user text, or fails for "boom".
type fakeStrategy struct {
	err error
	bad bool
}

func (fakeStrategy) Name() string { return "fake" }

func (f fakeStrategy) Score(_ context.Context, req chat.Request) (difficulty.Score, error) {
	if f.err != nil {
		return difficulty.Score{}, f.err
	}
	if f.bad {
		return difficulty.Score{P: 2}, nil
	}
	p, err := strconv.ParseFloat(req.LastUserText(), 64)
	if err != nil {
		return difficulty.Score{}, err
	}
	return difficulty.Score{P: p}, nil
}

func sampleOf(texts ...string) []chat.Request {
	reqs := make([]chat.Request, len(texts))
	for i, s := range texts {
		reqs[i] = chat.Request{Messages: []chat.Message{chat.TextMessage(chat.RoleUser, s)}}
	}
	return reqs
}

func TestCalibrate_Calibrate_Thresholds(t *testing.T) {
	t.Parallel()
	distinct := sampleOf("0.3", "0.9", "0.5", "0.7")
	tests := []struct {
		name      string
		sample    []chat.Request
		ratio     float64
		wantThr   float64
		wantRatio float64
	}{
		{name: "half", sample: distinct, ratio: 0.5, wantThr: 0.7, wantRatio: 0.5},
		{name: "all strong", sample: distinct, ratio: 1, wantThr: 0.3, wantRatio: 1},
		{name: "none strong", sample: distinct, ratio: 0, wantThr: 0.9000000000000001, wantRatio: 0},
		{name: "closest", sample: distinct, ratio: 0.4, wantThr: 0.7, wantRatio: 0.5},
		{name: "equal distance picks lower ratio", sample: distinct, ratio: 0.375, wantThr: 0.9, wantRatio: 0.25},
		{
			name:      "ties kept together",
			sample:    sampleOf("0.5", "0.1", "0.5", "0.1"),
			ratio:     0.25,
			wantThr:   0.5000000000000001,
			wantRatio: 0,
		},
		{name: "single", sample: sampleOf("0.4"), ratio: 0.9, wantThr: 0.4, wantRatio: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := calibrate.Calibrate(t.Context(), fakeStrategy{}, tc.sample, tc.ratio)
			require.NoError(t, err)
			assert.InDelta(t, tc.wantThr, float64(res.Threshold), 1e-15)
			assert.InDelta(t, tc.wantRatio, res.AchievedRatio, 1e-12)
			assert.Equal(t, len(tc.sample), res.N)
			assert.Equal(t, "fake", res.Strategy)
		})
	}
}

func TestCalibrate_Calibrate_Errors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name     string
		cancel   bool
		strategy difficulty.Strategy
		sample   []chat.Request
		ratio    float64
		want     error
		wantMsg  string
	}{
		{
			name:     "ratio too high",
			strategy: fakeStrategy{},
			sample:   sampleOf("0.1"),
			ratio:    1.1,
			wantMsg:  "outside [0,1]",
		},
		{
			name:     "ratio negative",
			strategy: fakeStrategy{},
			sample:   sampleOf("0.1"),
			ratio:    -0.1,
			wantMsg:  "outside [0,1]",
		},
		{
			name:     "ratio nan",
			strategy: fakeStrategy{},
			sample:   sampleOf("0.1"),
			ratio:    nan(),
			wantMsg:  "outside [0,1]",
		},
		{name: "empty sample", strategy: fakeStrategy{}, ratio: 0.5, wantMsg: "empty sample"},
		{name: "strategy error", strategy: fakeStrategy{err: boom}, sample: sampleOf("0.1"), ratio: 0.5, want: boom},
		{
			name:     "invalid score",
			strategy: fakeStrategy{bad: true},
			sample:   sampleOf("0.1"),
			ratio:    0.5,
			wantMsg:  "outside [0,1]",
		},
		{
			name:     "cancelled",
			cancel:   true,
			strategy: fakeStrategy{},
			sample:   sampleOf("0.1"),
			ratio:    0.5,
			want:     context.Canceled,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			_, err := calibrate.Calibrate(ctx, tc.strategy, tc.sample, tc.ratio)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			if tc.wantMsg != "" {
				assert.ErrorContains(t, err, tc.wantMsg)
			}
		})
	}
}

func nan() float64 {
	var z float64
	return z / z
}

func TestCalibrate_Result_Snippet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		thr  float64
		want string
	}{
		{name: "four digits", thr: 0.6234, want: "routes:\n  - name: smart\n    threshold: 0.6234\n"},
		{name: "one", thr: 1, want: "routes:\n  - name: smart\n    threshold: 1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := calibrate.Result{Threshold: difficulty.Threshold(tc.thr)}.Snippet("smart")
			assert.Equal(t, tc.want, got)
		})
	}
}
