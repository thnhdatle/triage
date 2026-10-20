// SPDX-License-Identifier: Apache-2.0

package fakestrategy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/testutil/fakestrategy"
)

var _ difficulty.Strategy = (*fakestrategy.Strategy)(nil)

func TestStrategy_Score_Fixed(t *testing.T) {
	t.Parallel()
	s := fakestrategy.Fixed("bert", 0.7)
	assert.Equal(t, "bert", s.Name())
	got, err := s.Score(context.Background(), chat.Request{})
	require.NoError(t, err)
	assert.InDelta(t, 0.7, got.P, 1e-12)
	assert.Equal(t, 1, s.Calls())
}

func TestStrategy_Score_FunctionOfRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want float64
	}{
		{name: "short", text: "ab", want: 0.2},
		{name: "longer", text: "abcde", want: 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := fakestrategy.New("len", func(_ context.Context, req chat.Request) (difficulty.Score, error) {
				return difficulty.Score{P: float64(len(req.LastUserText())) / 10}, nil
			})
			req := chat.Request{Messages: []chat.Message{chat.TextMessage(chat.RoleUser, tc.text)}}
			got, err := s.Score(context.Background(), req)
			require.NoError(t, err)
			assert.InDelta(t, tc.want, got.P, 1e-12)
			assert.Equal(t, 1, s.Calls())
		})
	}
}

func TestStrategy_Score_Failing(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	s := fakestrategy.Failing("mf", boom)
	_, err := s.Score(context.Background(), chat.Request{})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, s.Calls())
}

func TestStrategy_Score_BlockingUntilContextDone(t *testing.T) {
	t.Parallel()
	s := fakestrategy.Blocking("slow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := s.Score(ctx, chat.Request{})
		errc <- err
	}()
	select {
	case <-s.Entered():
	case <-time.After(5 * time.Second):
		t.Fatal("Score never started")
	}
	select {
	case <-errc:
		t.Fatal("Score returned before cancellation")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-errc:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Score did not return after cancellation")
	}
	assert.Equal(t, 1, s.Calls())
}
