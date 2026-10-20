// SPDX-License-Identifier: Apache-2.0

package artifact_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	app "github.com/thnhdatle/triage/internal/app/artifact"
	dom "github.com/thnhdatle/triage/internal/domain/artifact"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

func TestNew_Validation(t *testing.T) {
	t.Parallel()
	f, s := &fakeFetcher{}, &memStore{}
	bad := testManifest()
	bad.Entries[0].Files = append(bad.Entries[0].Files, dom.File{Path: "optimizer.pt"})
	traversal := testManifest()
	traversal.Entries[2].Files[0].Path = "../x"
	noRev := testManifest()
	noRev.Entries[0].Revision = ""
	tests := []struct {
		name string
		call func() error
		want error
	}{
		{"nil fetcher", func() error { _, e := app.New(testManifest(), nil, nil, s, "d"); return e }, app.ErrInvalid},
		{"nil store", func() error { _, e := app.New(testManifest(), f, nil, nil, "d"); return e }, app.ErrInvalid},
		{"empty cache", func() error { _, e := app.New(testManifest(), f, nil, s, ""); return e }, app.ErrInvalid},
		{"empty manifest", func() error { _, e := app.New(dom.Manifest{}, f, nil, s, "d"); return e }, app.ErrInvalid},
		{"nil deriver", func() error {
			_, e := app.New(testManifest(), f, []dom.Deriver{nil}, s, "d")
			return e
		}, app.ErrInvalid},
		{"no revision", func() error { _, e := app.New(noRev, f, nil, s, "d"); return e }, app.ErrInvalid},
		{"optimizer.pt", func() error { _, e := app.New(bad, f, nil, s, "d"); return e }, dom.ErrNotAllowed},
		{"traversal", func() error { _, e := app.New(traversal, f, nil, s, "d"); return e }, dom.ErrNotAllowed},
		{"ok", func() error {
			_, e := app.New(testManifest(), f, nil, s, "d", app.WithClock(clock.Real()), app.WithVerifier(app.FileVerifier{}))
			return e
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.call()
			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestService_Pull_Scenarios(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		names       []string
		int8        bool
		wantFetches int
		wantDerived []dom.DerivedKind
	}{
		{"all without int8", []string{"all"}, false, 4, []dom.DerivedKind{dom.KindONNXFP32, dom.KindMFVector}},
		{"all with int8", []string{"all"}, true, 4, []dom.DerivedKind{dom.KindONNXFP32, dom.KindONNXINT8, dom.KindMFVector}},
		{"bert only", []string{"bert"}, false, 2, []dom.DerivedKind{dom.KindONNXFP32}},
		{"mf with int8 flag", []string{"mf"}, true, 1, []dom.DerivedKind{dom.KindMFVector}},
		{"ner has no deriver", []string{"ner"}, true, 1, nil},
		{
			"duplicates collapse",
			[]string{"ner", "all", "ner"},
			false, 4,
			[]dom.DerivedKind{dom.KindONNXFP32, dom.KindMFVector},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			rep, err := r.svc.Pull(context.Background(), tc.names, app.PullOptions{INT8: tc.int8})
			require.NoError(t, err)
			assert.Len(t, r.fetcher.calls, tc.wantFetches)
			var got []dom.DerivedKind
			for _, e := range rep.Entries {
				for _, d := range e.DerivedBuilt {
					got = append(got, d.Kind)
					assert.NotEmpty(t, d.ConverterVersion)
					assert.NotEmpty(t, d.Path)
				}
			}
			assert.Equal(t, tc.wantDerived, got)
			for _, c := range r.fetcher.calls {
				assert.NotEqual(t, "optimizer.pt", c.Path)
			}
			for _, k := range tc.wantDerived {
				assert.Equal(t, 1, r.derivers[k].calls)
			}
			if tc.int8 {
				return
			}
			assert.Zero(t, r.derivers[dom.KindONNXINT8].calls)
		})
	}
}

func TestService_Pull_RecordsLocalManifestIdempotent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	_, err := r.svc.Pull(ctx, []string{"all"}, app.PullOptions{INT8: true})
	require.NoError(t, err)
	first := r.store.m
	bert, ok := first.Entry("bert")
	require.True(t, ok)
	require.Len(t, bert.Derived, 2)
	assert.Equal(t, "g1", bert.Derived[0].ConverterVersion)
	assert.Equal(t, "graph", bert.Derived[0].BuiltBy)
	saves := r.store.saves

	rep, err := r.svc.Pull(ctx, []string{"all"}, app.PullOptions{INT8: true})
	require.NoError(t, err)
	assert.Equal(t, first, r.store.m)
	assert.Equal(t, saves, r.store.saves, "no rebuild, no save")
	assert.Len(t, r.fetcher.calls, 4, "second pull fetches nothing")
	assert.Equal(t, []string{"config.json", "model.safetensors"}, rep.Entries[0].Skipped)
	assert.Empty(t, rep.Entries[0].Fetched)
	assert.Len(t, rep.Entries[0].DerivedSkipped, 2)
}

func TestService_Pull_RebuildsWhenDerivedChanged(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	_, err := r.svc.Pull(ctx, []string{"mf"}, app.PullOptions{})
	require.NoError(t, err)
	mf, _ := r.store.m.Entry("mf")
	require.NoError(t, os.WriteFile(mf.Derived[0].Path, []byte("tampered"), 0o600))
	_, err = r.svc.Pull(ctx, []string{"mf"}, app.PullOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, r.derivers[dom.KindMFVector].calls)
}

func TestService_Pull_SkipsVerifiedFiles(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	dir := filepath.Join(r.dir, "ner", "r3", "onnx")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.onnx"), content("onnx/model.onnx"), 0o600))
	rep, err := r.svc.Pull(context.Background(), []string{"ner"}, app.PullOptions{})
	require.NoError(t, err)
	assert.Empty(t, r.fetcher.calls)
	assert.Equal(t, []string{"onnx/model.onnx"}, rep.Entries[0].Skipped)
}

func TestService_Pull_RefetchesCorruptExistingFile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	dir := filepath.Join(r.dir, "ner", "r3", "onnx")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("bad"), 0o600))
	rep, err := r.svc.Pull(context.Background(), []string{"ner"}, app.PullOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"onnx/model.onnx"}, rep.Entries[0].Fetched)
}

func TestService_Pull_Errors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		names   []string
		setup   func(r *rig)
		want    error
		wantMsg string
	}{
		{"unknown name", []string{"gpt"}, func(*rig) {}, app.ErrUnknownName, `"gpt"`},
		{"no names", nil, func(*rig) {}, app.ErrInvalid, ""},
		{"fetch error", []string{"bert"}, func(r *rig) { r.fetcher.err = boom }, boom, "pull bert: fetch config.json"},
		{
			"checksum mismatch",
			[]string{"ner"},
			func(r *rig) { r.fetcher.corrupt = true }, dom.ErrChecksum,
			"verify onnx/model.onnx",
		},
		{"derive error", []string{"mf"}, func(r *rig) { r.derivers[dom.KindMFVector].err = boom }, boom, "derive mf-vector"},
		{"load error", []string{"mf"}, func(r *rig) { r.store.loadErr = boom }, boom, "load local manifest"},
		{"save error", []string{"mf"}, func(r *rig) { r.store.saveErr = boom }, boom, "save local manifest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			tc.setup(r)
			_, err := r.svc.Pull(context.Background(), tc.names, app.PullOptions{})
			require.ErrorIs(t, err, tc.want)
			assert.ErrorContains(t, err, tc.wantMsg)
		})
	}
}

func TestService_Pull_MissingDeriver(t *testing.T) {
	t.Parallel()
	svc, err := app.New(testManifest(), &fakeFetcher{}, nil, &memStore{}, t.TempDir())
	require.NoError(t, err)
	_, err = svc.Pull(context.Background(), []string{"mf"}, app.PullOptions{})
	require.ErrorIs(t, err, app.ErrNoDeriver)
}

func TestService_Pull_NeverRequestsOptimizer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.svc.Pull(context.Background(), []string{"all"}, app.PullOptions{INT8: true})
	require.NoError(t, err)
	for _, c := range r.fetcher.calls {
		assert.NotContains(t, c.Path, "optimizer.pt")
	}
	_, statErr := os.Stat(filepath.Join(r.dir, "bert", "r1", "optimizer.pt"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestService_Pull_Durations(t *testing.T) {
	t.Parallel()
	fc := clock.NewFake(time.Unix(0, 0))
	svc, err := app.New(testManifest(), &fakeFetcher{}, nil, &memStore{}, t.TempDir(), app.WithClock(fc))
	require.NoError(t, err)
	rep, err := svc.Pull(context.Background(), []string{"ner"}, app.PullOptions{})
	require.NoError(t, err)
	assert.Zero(t, rep.Duration)
	assert.Len(t, rep.Entries, 1)
}

func TestFileVerifier_Verify(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(p, content("x"), 0o600))
	good := fileOf("x")
	badSize := good
	badSize.Size++
	badSum := good
	badSum.SHA256 = "00"
	tests := []struct {
		name string
		path string
		file dom.File
		want error
		miss bool
	}{
		{"ok", p, good, nil, false},
		{"unchecked", p, dom.File{}, nil, false},
		{"size", p, badSize, dom.ErrChecksum, false},
		{"sha", p, badSum, dom.ErrChecksum, false},
		{"missing", filepath.Join(dir, "none"), good, nil, true},
		{"directory", dir, good, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := app.FileVerifier{}.Verify(tc.path, tc.file)
			switch {
			case tc.miss:
				require.ErrorIs(t, err, os.ErrNotExist)
			case tc.want != nil:
				require.ErrorIs(t, err, tc.want)
			case tc.name == "directory":
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}
		})
	}
}
