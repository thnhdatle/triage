// SPDX-License-Identifier: Apache-2.0

package hfhub_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thnhdatle/triage/internal/adapter/hfhub"
	"github.com/thnhdatle/triage/internal/domain/artifact"
)

const rev = "86237e3df400762178ea98379477b8296e66d5e4"

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func entry(files ...artifact.File) artifact.Entry {
	return artifact.Entry{Name: "bert", HFRepo: "routellm/bert", Revision: rev, Files: files}
}

func file(p string, body []byte) artifact.File {
	return artifact.File{Path: p, SHA256: sum(body), Size: int64(len(body))}
}

func newFetcher(t *testing.T, srv *httptest.Server) *hfhub.Fetcher {
	t.Helper()
	f, err := hfhub.New(hfhub.WithBaseURL(srv.URL), hfhub.WithAllowedHosts("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestFetchOK(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat("weights", 1000))
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	f := newFetcher(t, srv)
	dir := t.TempDir()
	file := file("onnx/model.onnx", body)
	if err := f.Fetch(context.Background(), entry(file), file, dir); err != nil {
		t.Fatal(err)
	}
	if want := "/routellm/bert/resolve/" + rev + "/onnx/model.onnx"; gotPath != want {
		t.Fatalf("path %q want %q", gotPath, want)
	}
	got, err := os.ReadFile(filepath.Join(dir, "onnx", "model.onnx"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("content mismatch: %v", err)
	}
	if l := leftovers(t, dir); len(l) != 1 {
		t.Fatalf("leftovers %v", l)
	}
}

func TestFetchFollowsRedirectToAllowedHost(t *testing.T) {
	t.Parallel()
	body := []byte("cdn body")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credentials forwarded")
		}
		_, _ = w.Write(body)
	}))
	defer cdn.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+"/blob", http.StatusFound)
	}))
	defer hub.Close()
	f := newFetcher(t, hub)
	file := file("a.bin", body)
	if err := f.Fetch(context.Background(), entry(file), file, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestRedirectToPlainHTTPRejected(t *testing.T) {
	t.Parallel()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://huggingface.co/x", http.StatusFound)
	}))
	defer hub.Close()
	f, err := hfhub.New(hfhub.WithBaseURL(hub.URL), hfhub.WithAllowedHosts("127.0.0.1", "huggingface.co"))
	if err != nil {
		t.Fatal(err)
	}
	file := file("a.bin", []byte("x"))
	dir := t.TempDir()
	if err := f.Fetch(context.Background(), entry(file), file, dir); !errors.Is(err, hfhub.ErrDownload) {
		t.Fatalf("err %v", err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Fatalf("leftovers %v", l)
	}
}

func TestRedirectToNonAllowListedHost(t *testing.T) {
	t.Parallel()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:1/x", http.StatusFound)
	}))
	defer hub.Close()
	f := newFetcher(t, hub)
	file := file("a.bin", []byte("x"))
	err := f.Fetch(context.Background(), entry(file), file, t.TempDir())
	if !errors.Is(err, hfhub.ErrDownload) || !strings.Contains(err.Error(), "not allow-listed") {
		t.Fatalf("err %v", err)
	}
}

func TestRedirectLoop(t *testing.T) {
	t.Parallel()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	}))
	defer hub.Close()
	f := newFetcher(t, hub)
	file := file("a.bin", []byte("x"))
	if err := f.Fetch(context.Background(), entry(file), file, t.TempDir()); !errors.Is(err, hfhub.ErrDownload) {
		t.Fatalf("err %v", err)
	}
}

func TestBodyVerification(t *testing.T) {
	t.Parallel()
	want := []byte("expected content")
	cases := map[string][]byte{
		"wrong content": []byte("expected CONTENT!"),
		"short":         want[:5],
		"oversized":     append(append([]byte{}, want...), "extra"...),
	}
	for name, served := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(served)
			}))
			defer srv.Close()
			f := newFetcher(t, srv)
			file := file("a.bin", want)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := f.Fetch(context.Background(), entry(file), file, dir)
			if !errors.Is(err, artifact.ErrChecksum) {
				t.Fatalf("err %v", err)
			}
			if old, _ := os.ReadFile(filepath.Join(dir, "a.bin")); string(old) != "old" {
				t.Fatal("target replaced by unverified content")
			}
			if l := leftovers(t, dir); len(l) != 1 {
				t.Fatalf("leftovers %v", l)
			}
		})
	}
}

func TestHTTPStatusAndTransportErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	f := newFetcher(t, srv)
	file := file("a.bin", []byte("x"))
	if err := f.Fetch(context.Background(), entry(file), file, t.TempDir()); !errors.Is(err, hfhub.ErrDownload) {
		t.Fatalf("status err %v", err)
	}
	srv.Close()
	if err := f.Fetch(context.Background(), entry(file), file, t.TempDir()); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestContextCanceledMidBody(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("part"))
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	f := newFetcher(t, srv)
	file := file("a.bin", []byte("partial and more"))
	dir := t.TempDir()
	if err := f.Fetch(ctx, entry(file), file, dir); err == nil {
		t.Fatal("expected error")
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Fatalf("leftovers %v", l)
	}
}

func TestValidation(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	f := newFetcher(t, srv)
	good := file("a.bin", []byte("x"))
	mk := func(mod func(*artifact.Entry, *artifact.File)) (artifact.Entry, artifact.File) {
		e, fl := entry(good), good
		mod(&e, &fl)
		return e, fl
	}
	cases := map[string]struct {
		mod  func(*artifact.Entry, *artifact.File)
		want error
	}{
		"optimizer": {func(e *artifact.Entry, fl *artifact.File) {
			fl.Path = "optimizer.pt"
			e.Files = []artifact.File{*fl}
		}, artifact.ErrNotAllowed},
		"nested optimizer": {func(e *artifact.Entry, fl *artifact.File) {
			fl.Path = "sub/optimizer.pt"
			e.Files = []artifact.File{*fl}
		}, artifact.ErrNotAllowed},
		"not listed": {func(_ *artifact.Entry, fl *artifact.File) { fl.Path = "other.bin" }, artifact.ErrNotAllowed},
		"traversal": {func(e *artifact.Entry, fl *artifact.File) {
			fl.Path = "../a.bin"
			e.Files = []artifact.File{*fl}
		}, artifact.ErrNotAllowed},
		"branch revision": {func(e *artifact.Entry, _ *artifact.File) { e.Revision = "main" }, hfhub.ErrInvalid},
		"short revision":  {func(e *artifact.Entry, _ *artifact.File) { e.Revision = rev[:7] }, hfhub.ErrInvalid},
		"bad repo":        {func(e *artifact.Entry, _ *artifact.File) { e.HFRepo = "../etc" }, hfhub.ErrInvalid},
		"no sha":          {func(_ *artifact.Entry, fl *artifact.File) { fl.SHA256 = "" }, hfhub.ErrInvalid},
		"no size":         {func(_ *artifact.Entry, fl *artifact.File) { fl.Size = 0 }, hfhub.ErrInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, fl := mk(c.mod)
			dir := t.TempDir()
			if err := f.Fetch(context.Background(), e, fl, dir); !errors.Is(err, c.want) {
				t.Fatalf("err %v want %v", err, c.want)
			}
			if l := leftovers(t, dir); len(l) != 0 {
				t.Fatalf("leftovers %v", l)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests made for invalid input", hits.Load())
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	cases := map[string][]hfhub.Option{
		"nil option":        {nil},
		"bad url":           {hfhub.WithBaseURL("::")},
		"no host":           {hfhub.WithBaseURL("/relative")},
		"http remote":       {hfhub.WithBaseURL("http://huggingface.co")},
		"host not in list":  {hfhub.WithBaseURL("https://example.com")},
		"suffix lookalike":  {hfhub.WithBaseURL("https://evilhf.co")},
		"empty allow-list":  {hfhub.WithAllowedHosts()},
		"base on other tld": {hfhub.WithBaseURL("https://huggingface.co.evil.com")},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := hfhub.New(opts...); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	for _, ok := range []string{"https://huggingface.co", "https://cdn-lfs.hf.co", "https://cas-bridge.xethub.hf.co"} {
		if _, err := hfhub.New(hfhub.WithBaseURL(ok)); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	if _, err := hfhub.New(hfhub.WithHTTPClient(http.DefaultClient)); err != nil {
		t.Fatal(err)
	}
}

func TestMkdirAndRenameFailures(t *testing.T) {
	t.Parallel()
	body := []byte("x")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	f := newFetcher(t, srv)
	file := file("sub/a.bin", body)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sub"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(context.Background(), entry(file), file, dir); err == nil {
		t.Fatal("expected mkdir error")
	}
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, "sub", "a.bin", "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(context.Background(), entry(file), file, dir2); err == nil {
		t.Fatal("expected rename error")
	}
	if l := leftovers(t, dir2); len(l) != 0 {
		t.Fatalf("leftovers %v", l)
	}
}

func TestEscapedPath(t *testing.T) {
	t.Parallel()
	body := []byte("x")
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.EscapedPath()
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	f := newFetcher(t, srv)
	file := file("dir/a b.bin", body)
	if err := f.Fetch(context.Background(), entry(file), file, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(raw, "/dir/a%20b.bin") {
		t.Fatalf("escaped path %q", raw)
	}
}
