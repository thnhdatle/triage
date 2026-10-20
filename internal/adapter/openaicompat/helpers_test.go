// SPDX-License-Identifier: Apache-2.0

package openaicompat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/openaicompat"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/fakellm"
)

var update = flag.Bool("update", false, "update golden files")

// newFake starts a fakellm server and returns its /v1 base URL.
func newFake(t *testing.T) string {
	t.Helper()
	srv, err := fakellm.New(fakellm.WithRecord(true, 100))
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL + "/v1"
}

// newServer starts a server answering every request with h.
func newServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

// newClient returns a client for base whose idle connections are closed at
// the end of the test.
func newClient(t *testing.T, base string, opts ...openaicompat.Option) *openaicompat.Client {
	t.Helper()
	c, err := openaicompat.New(base, opts...)
	require.NoError(t, err)
	t.Cleanup(c.CloseIdleConnections)
	return c
}

// scenario returns options selecting a fakellm scenario by header.
func scenario(name string, extra map[string]string) openaicompat.Option {
	h := map[string]string{"X-Fake-Scenario": name}
	for k, v := range extra {
		h[k] = v
	}
	return openaicompat.WithExtraHeaders(h)
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func userReq(model, text string) chat.Request {
	return chat.Request{Model: model, Messages: []chat.Message{chat.TextMessage(chat.RoleUser, text)}}
}

// fakeRequests returns the request log of the fakellm behind base.
func fakeRequests(t *testing.T, base string) []fakellm.Record {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxT(t), http.MethodGet, base[:len(base)-len("/v1")]+"/_fake/requests", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out struct {
		Requests []fakellm.Record `json:"requests"`
	}
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &out), string(data))
	return out.Requests
}

// golden compares got with testdata/golden/<name>.golden, rewriting it with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if *update {
		var buf bytes.Buffer
		require.NoError(t, json.Indent(&buf, got, "", "  "))
		buf.WriteByte('\n')
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}

func ptr[T any](v T) *T { return &v }
