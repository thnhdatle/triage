// SPDX-License-Identifier: Apache-2.0

package provider_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/provider"
)

func TestError_Error_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *provider.Error
		want string
	}{
		{name: "no message", err: &provider.Error{Kind: provider.ErrTimeout}, want: "provider: timeout"},
		{
			name: "with message",
			err:  &provider.Error{Kind: provider.ErrRateLimited, Message: "slow down"},
			want: "provider: rate limited: slow down",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

func TestError_Unwrap_Sentinels(t *testing.T) {
	t.Parallel()
	sentinels := []error{
		provider.ErrRateLimited, provider.ErrUpstreamUnavailable, provider.ErrBadRequest,
		provider.ErrTimeout, provider.ErrContextLength,
	}
	for _, s := range sentinels {
		t.Run(s.Error(), func(t *testing.T) {
			t.Parallel()
			e := &provider.Error{Kind: s}
			assert.Equal(t, s, e.Unwrap())
			require.ErrorIs(t, e, s)
			for _, other := range sentinels {
				if !errors.Is(other, s) {
					assert.NotErrorIs(t, e, other)
				}
			}
		})
	}
}

func TestRetryAfter_Err_Table(t *testing.T) {
	t.Parallel()
	pe := &provider.Error{Kind: provider.ErrRateLimited, RetryAfter: 3 * time.Second}
	tests := []struct {
		name string
		err  error
		want time.Duration
	}{
		{name: "direct", err: pe, want: 3 * time.Second},
		{name: "wrapped", err: fmt.Errorf("attempt: %w", pe), want: 3 * time.Second},
		{name: "absent", err: &provider.Error{Kind: provider.ErrTimeout}, want: 0},
		{name: "foreign error", err: errors.New("boom"), want: 0},
		{name: "nil", err: nil, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, provider.RetryAfter(tc.err))
		})
	}
}

func TestStatusCode_Err_Table(t *testing.T) {
	t.Parallel()
	pe := &provider.Error{Kind: provider.ErrBadRequest, StatusCode: 400}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "direct", err: pe, want: 400},
		{name: "wrapped", err: fmt.Errorf("attempt: %w", pe), want: 400},
		{name: "no status", err: &provider.Error{Kind: provider.ErrTimeout}, want: 0},
		{name: "foreign error", err: errors.New("boom"), want: 0},
		{name: "nil", err: nil, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, provider.StatusCode(tc.err))
		})
	}
}

func TestIsRetryable_Err_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "rate limited", err: &provider.Error{Kind: provider.ErrRateLimited}, want: true},
		{name: "unavailable", err: &provider.Error{Kind: provider.ErrUpstreamUnavailable}, want: true},
		{name: "timeout", err: &provider.Error{Kind: provider.ErrTimeout}, want: true},
		{name: "bad request", err: &provider.Error{Kind: provider.ErrBadRequest}, want: false},
		{name: "context length", err: &provider.Error{Kind: provider.ErrContextLength}, want: false},
		{
			name: "bad request with 5xx status is not retryable",
			err:  &provider.Error{Kind: provider.ErrBadRequest, StatusCode: 500},
			want: false,
		},
		{name: "wrapped rate limit", err: fmt.Errorf("x: %w", &provider.Error{Kind: provider.ErrRateLimited}), want: true},
		{name: "bare sentinel", err: provider.ErrTimeout, want: true},
		{name: "foreign 5xx kind", err: &provider.Error{Kind: errors.New("other"), StatusCode: 502}, want: true},
		{name: "foreign 4xx kind", err: &provider.Error{Kind: errors.New("other"), StatusCode: 404}, want: false},
		{name: "foreign 499", err: &provider.Error{Kind: errors.New("other"), StatusCode: 499}, want: false},
		{name: "foreign 500", err: &provider.Error{Kind: errors.New("other"), StatusCode: 500}, want: true},
		{name: "foreign error", err: errors.New("boom"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, provider.IsRetryable(tc.err))
		})
	}
}
