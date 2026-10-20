// SPDX-License-Identifier: Apache-2.0

package anthropic_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/anthropic"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

func TestClient_Complete_ResponseGolden(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"text", "tool_use", "tool_only", "max_tokens", "refusal", "stop_sequence"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp, err := newFixtureServer(t).client(t, name).Complete(ctxT(t), userReq("x"))
			require.NoError(t, err)
			assert.Equal(t, epoch.Unix(), resp.Created)
			goldenValue(t, "response_"+name, resp)
		})
	}
}

func TestClient_Complete_UsageWithCache(t *testing.T) {
	t.Parallel()
	resp, err := newFixtureServer(t).client(t, "text").Complete(ctxT(t), userReq("x"))
	require.NoError(t, err)
	assert.Equal(t, chat.Usage{PromptTokens: 21 + 100 + 2048, CompletionTokens: 9, CachedPromptTokens: 2048}, resp.Usage)
	assert.Equal(t, "claude-haiku-5-5", resp.Model)
	assert.Equal(t, "Hanoi is the capital of Vietnam.", resp.Choices[0].Message.Text())
}

func TestClient_Complete_ErrorFixtures_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fixture    string
		kind       error
		status     int
		retryAfter time.Duration
	}{
		{fixture: "error_429", kind: provider.ErrRateLimited, status: 429, retryAfter: 12 * time.Second},
		{fixture: "error_529", kind: provider.ErrUpstreamUnavailable, status: 529},
		{fixture: "error_500", kind: provider.ErrUpstreamUnavailable, status: 500},
		{fixture: "error_504", kind: provider.ErrTimeout, status: 504},
		{fixture: "error_prompt_too_long", kind: provider.ErrContextLength, status: 400},
		{fixture: "error_invalid", kind: provider.ErrBadRequest, status: 400},
		{fixture: "error_401", kind: provider.ErrBadRequest, status: 401},
		{fixture: "error_413", kind: provider.ErrBadRequest, status: 413},
		{fixture: "malformed", kind: provider.ErrUpstreamUnavailable, status: 200},
		{fixture: "not_message", kind: provider.ErrUpstreamUnavailable, status: 200},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			c := newFixtureServer(t).client(t, tc.fixture)
			_, err := c.Complete(ctxT(t), userReq("x"))
			require.ErrorIs(t, err, tc.kind)
			assert.Equal(t, tc.status, provider.StatusCode(err))
			assert.Equal(t, tc.retryAfter, provider.RetryAfter(err))
			assert.NotContains(t, err.Error(), "secret user text", "upstream messages are never surfaced")
			if tc.fixture != "malformed" && tc.fixture != "not_message" {
				_, err = c.Stream(ctxT(t), userReq("x"))
				require.ErrorIs(t, err, tc.kind)
			}
		})
	}
}

func TestClient_Complete_RetryAfter_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "fraction", value: "1.5", want: 1500 * time.Millisecond},
		{name: "negative", value: "-1", want: 0},
		{name: "date", value: epoch.Add(time.Minute).Format(http.TimeFormat), want: time.Minute},
		{name: "past date", value: epoch.Add(-time.Minute).Format(http.TimeFormat), want: 0},
		{name: "garbage", value: "later", want: 0},
		{name: "absent", value: "", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.value != "" {
					w.Header().Set("Retry-After", tc.value)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			})
			c := newClient(t, anthropic.WithBaseURL(base), anthropic.WithClock(clock.NewFake(epoch)))
			_, err := c.Complete(ctxT(t), userReq("x"))
			require.ErrorIs(t, err, provider.ErrRateLimited)
			assert.Equal(t, tc.want, provider.RetryAfter(err))
		})
	}
}

func TestClient_Complete_Timeouts(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	base := newServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	c := newClient(t, anthropic.WithBaseURL(base), anthropic.WithTimeout(100*time.Millisecond))
	_, err := c.Complete(ctxT(t), userReq("x"))
	require.ErrorIs(t, err, provider.ErrTimeout)
	_, err = c.Stream(ctxT(t), userReq("x"))
	require.ErrorIs(t, err, provider.ErrTimeout, "header timeout")

	cause := errors.New("attempt timeout")
	ctx, cancel := context.WithCancelCause(ctxT(t))
	time.AfterFunc(50*time.Millisecond, func() { cancel(cause) })
	_, err = newClient(t, anthropic.WithBaseURL(base)).Complete(ctx, userReq("x"))
	require.ErrorIs(t, err, cause)
}

func TestClient_Complete_Unreachable(t *testing.T) {
	t.Parallel()
	c := newClient(t, anthropic.WithBaseURL("http://127.0.0.1:1/v1"))
	_, err := c.Complete(ctxT(t), userReq("x"))
	require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
}

func TestClient_Complete_BodyTooLarge(t *testing.T) {
	t.Parallel()
	c := newFixtureServer(t).client(t, "text", anthropic.WithMaxResponseBytes(16))
	_, err := c.Complete(ctxT(t), userReq("x"))
	require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
}

func TestClient_Embed_Unsupported(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	_, err := c.Embed(ctxT(t), provider.EmbedRequest{Model: "m", Input: []string{"x"}})
	require.ErrorIs(t, err, provider.ErrBadRequest)
}

func TestClient_Models_Fixture(t *testing.T) {
	t.Parallel()
	fs := newFixtureServer(t)
	names, err := fs.client(t, "models").Models(ctxT(t))
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-5-5", "claude-haiku-5-5"}, names)
	r := fs.last(t)
	assert.Equal(t, "GET", r.method)
	assert.Equal(t, "/v1/models?limit=1000", r.path)
}

func TestClient_Models_Pagination(t *testing.T) {
	t.Parallel()
	base := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = io.WriteString(w, `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`)
		case "a":
			_, _ = io.WriteString(w, `{"data":[{"id":"b"}],"has_more":true,"last_id":""}`)
		}
	})
	names, err := newClient(t, anthropic.WithBaseURL(base)).Models(ctxT(t))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, names)
}

func TestClient_Models_PageLimit(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	base := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"x"}],"has_more":true,"last_id":"x"}`)
	})
	names, err := newClient(t, anthropic.WithBaseURL(base)).Models(ctxT(t))
	require.NoError(t, err)
	assert.Len(t, names, 20)
	assert.Equal(t, int32(20), calls.Load())
}

func TestClient_Models_Errors_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		kind error
	}{
		{name: "no data", body: `{}`, kind: provider.ErrUpstreamUnavailable},
		{name: "not json", body: `<html>`, kind: provider.ErrUpstreamUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := newServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) })
			_, err := newClient(t, anthropic.WithBaseURL(base)).Models(ctxT(t))
			require.ErrorIs(t, err, tc.kind)
		})
	}
	_, err := newFixtureServer(t).client(t, "error_401").Models(ctxT(t))
	require.ErrorIs(t, err, provider.ErrBadRequest)
}

func TestNew_Config_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opt  anthropic.Option
	}{
		{name: "relative url", opt: anthropic.WithBaseURL("/v1")},
		{name: "bad scheme", opt: anthropic.WithBaseURL("ws://x")},
		{name: "unparsable", opt: anthropic.WithBaseURL("http://[::1")},
		{name: "empty version", opt: anthropic.WithVersion("")},
		{name: "zero max tokens", opt: anthropic.WithDefaultMaxTokens(0)},
		{name: "zero timeout", opt: anthropic.WithTimeout(0)},
		{name: "negative idle", opt: anthropic.WithMaxIdleConnsPerHost(-1)},
		{name: "zero max bytes", opt: anthropic.WithMaxResponseBytes(0)},
		{name: "nil clock", opt: anthropic.WithClock(nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := anthropic.New(tc.opt)
			require.ErrorIs(t, err, anthropic.ErrConfig)
			assert.True(t, strings.HasPrefix(err.Error(), "anthropic: "))
		})
	}
}

func TestNew_HTTPClient(t *testing.T) {
	t.Parallel()
	fs := newFixtureServer(t)
	hc := &http.Client{}
	t.Cleanup(hc.CloseIdleConnections)
	c := fs.client(t, "text", anthropic.WithHTTPClient(hc), anthropic.WithMaxIdleConnsPerHost(1))
	_, err := c.Complete(ctxT(t), userReq("x"))
	require.NoError(t, err)
}
