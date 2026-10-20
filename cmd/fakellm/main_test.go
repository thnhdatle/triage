// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestHealthURL(t *testing.T) {
	t.Parallel()
	tests := []struct{ listen, want string }{
		{":8090", "http://127.0.0.1:8090/healthz"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/healthz"},
		{"10.0.0.1:9000", "http://10.0.0.1:9000/healthz"},
		{"bogus", "http://127.0.0.1:8090/healthz"},
	}
	for _, tc := range tests {
		t.Run(tc.listen, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, healthURL(tc.listen))
		})
	}
}

func TestHealthcheck(t *testing.T) {
	t.Parallel()
	ok := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	require.NoError(t, healthcheck(t.Context(), ok.URL))
	require.Error(t, healthcheck(t.Context(), bad.URL))
	require.Error(t, healthcheck(t.Context(), "http://127.0.0.1:1/healthz"))
}

func TestRun_BadFlags(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	assert.Equal(t, exitUsage, run(t.Context(), []string{"--nope"}, &stderr))
	assert.Equal(t, exitUsage, run(t.Context(), []string{"--record-limit=0"}, &stderr))
	assert.Equal(t, exitUsage, run(t.Context(), []string{"healthcheck", "--nope"}, &stderr))
}

func TestRun_ServesUntilCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--listen=127.0.0.1:0"}, &stderr) }()
	cancel()
	assert.Equal(t, 0, <-done)
}
