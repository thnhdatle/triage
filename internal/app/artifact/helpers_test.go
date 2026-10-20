// SPDX-License-Identifier: Apache-2.0

package artifact_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/thnhdatle/triage/internal/app/artifact"
	dom "github.com/thnhdatle/triage/internal/domain/artifact"
)

func content(p string) []byte { return []byte("bytes-of-" + p) }

func fileOf(p string) dom.File {
	sum := sha256.Sum256(content(p))
	return dom.File{Path: p, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content(p)))}
}

func testManifest() dom.Manifest {
	return dom.Manifest{SchemaVersion: 1, Entries: []dom.Entry{
		{
			Name: "bert", Revision: "r1", Files: []dom.File{fileOf("config.json"), fileOf("model.safetensors")},
			Derived: []dom.Derived{
				{Kind: dom.KindONNXFP32, BuiltBy: "graph", ConverterVersion: "g1"},
				{Kind: dom.KindONNXINT8, BuiltBy: "quant", ConverterVersion: "q1"},
			},
		},
		{
			Name: "mf", Revision: "r2", Files: []dom.File{fileOf("model.safetensors")},
			Derived: []dom.Derived{{Kind: dom.KindMFVector, BuiltBy: "go", ConverterVersion: "m1"}},
		},
		{Name: "ner", Revision: "r3", Files: []dom.File{fileOf("onnx/model.onnx")}},
	}}
}

type fetchCall struct{ Name, Path string }

type fakeFetcher struct {
	mu      sync.Mutex
	calls   []fetchCall
	err     error
	corrupt bool
}

func (f *fakeFetcher) Fetch(_ context.Context, e dom.Entry, file dom.File, dir string) error {
	f.mu.Lock()
	f.calls = append(f.calls, fetchCall{e.Name, file.Path})
	f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	data := content(file.Path)
	if f.corrupt {
		data = []byte("corrupt")
	}
	target := filepath.Join(dir, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o600)
}

type fakeDeriver struct {
	kind  dom.DerivedKind
	err   error
	calls int
}

func (d *fakeDeriver) Kind() dom.DerivedKind { return d.kind }

func (d *fakeDeriver) Derive(_ context.Context, e dom.Entry, dir string) (dom.Derived, error) {
	d.calls++
	if d.err != nil {
		return dom.Derived{}, d.err
	}
	p := filepath.Join(dir, string(d.kind)+".bin")
	data := []byte("derived-" + string(d.kind))
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return dom.Derived{}, err
	}
	sum := sha256.Sum256(data)
	return dom.Derived{Kind: d.kind, Path: p, SHA256: hex.EncodeToString(sum[:])}, nil
}

type memStore struct {
	mu      sync.Mutex
	m       dom.Manifest
	saves   int
	loadErr error
	saveErr error
}

func (s *memStore) Load(context.Context) (dom.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m, s.loadErr
}

func (s *memStore) Save(_ context.Context, m dom.Manifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.m = m
	s.saves++
	return nil
}

type rig struct {
	svc      *app.Service
	fetcher  *fakeFetcher
	store    *memStore
	derivers map[dom.DerivedKind]*fakeDeriver
	dir      string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		fetcher: &fakeFetcher{}, store: &memStore{}, dir: t.TempDir(),
		derivers: map[dom.DerivedKind]*fakeDeriver{
			dom.KindONNXFP32: {kind: dom.KindONNXFP32},
			dom.KindONNXINT8: {kind: dom.KindONNXINT8},
			dom.KindMFVector: {kind: dom.KindMFVector},
		},
	}
	ds := []dom.Deriver{r.derivers[dom.KindONNXFP32], r.derivers[dom.KindONNXINT8], r.derivers[dom.KindMFVector]}
	svc, err := app.New(testManifest(), r.fetcher, ds, r.store, r.dir)
	require.NoError(t, err)
	r.svc = svc
	return r
}
