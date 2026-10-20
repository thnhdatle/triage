// SPDX-License-Identifier: Apache-2.0

package anthropic_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/adapter/anthropic"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

var update = flag.Bool("update", false, "update golden files")

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// epoch is the fake clock time; translated responses carry it as created.
var epoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// fixtureHeader selects the recorded response served by the fixture server.
const fixtureHeader = "X-Fixture"

// recorded is one request received by the fixture server.
type recorded struct {
	method, path string
	header       http.Header
	body         []byte
}

// fixtureServer replays recorded Anthropic responses from testdata/anthropic.
// A fixture file holds "status: N", response headers, a blank line and the
// body, like a raw HTTP response. The fixture is chosen by the X-Fixture
// request header.
type fixtureServer struct {
	url  string
	mu   sync.Mutex
	reqs []recorded
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	ts := httptest.NewServer(http.HandlerFunc(fs.serve))
	t.Cleanup(ts.Close)
	fs.url = ts.URL + "/v1"
	return fs
}

func (fs *fixtureServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	fs.mu.Lock()
	fs.reqs = append(fs.reqs, recorded{method: r.Method, path: r.URL.RequestURI(), header: r.Header.Clone(), body: body})
	fs.mu.Unlock()
	status, header, payload, err := loadFixture(r.Header.Get(fixtureHeader))
	if err != nil {
		http.Error(w, err.Error(), http.StatusTeapot)
		return
	}
	for k, v := range header {
		w.Header()[k] = v
	}
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func loadFixture(name string) (int, http.Header, []byte, error) {
	data, err := os.ReadFile(filepath.Join("testdata", "anthropic", name+".http"))
	if err != nil {
		return 0, nil, nil, err
	}
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(data)))
	mh, err := tp.ReadMIMEHeader()
	if err != nil {
		return 0, nil, nil, err
	}
	status, err := strconv.Atoi(mh.Get("Status"))
	if err != nil {
		return 0, nil, nil, err
	}
	mh.Del("Status")
	body, err := io.ReadAll(tp.R)
	if err != nil {
		return 0, nil, nil, err
	}
	if !strings.HasPrefix(mh.Get("Content-Type"), "text/event-stream") {
		body = bytes.TrimRight(body, "\n")
	}
	return status, http.Header(mh), body, nil
}

func (fs *fixtureServer) last(t *testing.T) recorded {
	t.Helper()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.NotEmpty(t, fs.reqs)
	return fs.reqs[len(fs.reqs)-1]
}

// client returns a client for the fixture server serving fixture.
func (fs *fixtureServer) client(t *testing.T, fixture string, opts ...anthropic.Option) *anthropic.Client {
	t.Helper()
	all := make([]anthropic.Option, 0, 4+len(opts))
	all = append(all,
		anthropic.WithBaseURL(fs.url), anthropic.WithAPIKey("sk-ant-test"),
		anthropic.WithClock(clock.NewFake(epoch)),
		anthropic.WithExtraHeaders(map[string]string{fixtureHeader: fixture}),
	)
	return newClient(t, append(all, opts...)...)
}

func newClient(t *testing.T, opts ...anthropic.Option) *anthropic.Client {
	t.Helper()
	c, err := anthropic.New(opts...)
	require.NoError(t, err)
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func newServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// golden compares got (JSON) with testdata/golden/<name>.golden; -update
// rewrites it.
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

func goldenValue(t *testing.T, name string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	golden(t, name, data)
}

// collect reads a stream to its end.
func collect(t *testing.T, s provider.Stream) ([]chat.Chunk, error) {
	t.Helper()
	var out []chat.Chunk
	for {
		c, err := s.Recv()
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
}

func userReq(text string) chat.Request {
	return chat.Request{Model: "claude-haiku-5-5", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, text)}}
}

func ptr[T any](v T) *T { return &v }
