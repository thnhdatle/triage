// SPDX-License-Identifier: Apache-2.0

package artifact_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	app "github.com/thnhdatle/triage/internal/app/artifact"
	dom "github.com/thnhdatle/triage/internal/domain/artifact"
)

func byName(t *testing.T, ss []app.Status, name string) app.Status {
	t.Helper()
	for _, s := range ss {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status %s", name)
	return app.Status{}
}

func TestService_List_Empty(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ss, err := r.svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, ss, 3)
	bert := byName(t, ss, "bert")
	assert.False(t, bert.Complete())
	assert.Equal(t, app.FileMissing, bert.Files[0].State)
	assert.Equal(t, app.VariantNone, bert.Active)
	assert.Empty(t, bert.SuggestedTimeout)
	assert.Len(t, bert.Latency, 6)
	assert.Equal(t, "31 ms", bert.Latency[0].P50)
	assert.Equal(t, "61 ms", bert.Latency[3].P50)
	assert.Empty(t, byName(t, ss, "ner").Active)
}

func TestService_List_Variants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		int8        bool
		wantActive  string
		wantTimeout string
	}{
		{"fp32 only", false, "onnx-fp32", "500ms"},
		{"int8 built", true, "onnx-int8", "300ms"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			_, err := r.svc.Pull(context.Background(), []string{"all"}, app.PullOptions{INT8: tc.int8})
			require.NoError(t, err)
			ss, err := r.svc.List(context.Background())
			require.NoError(t, err)
			bert := byName(t, ss, "bert")
			assert.True(t, bert.Complete())
			assert.Equal(t, tc.wantActive, bert.Active)
			assert.Equal(t, tc.wantTimeout, bert.SuggestedTimeout)
			assert.Equal(t, tc.int8, bert.Derived[1].Present)
			assert.True(t, bert.Derived[0].Present)
			assert.NotEmpty(t, bert.Derived[0].Path)
			assert.True(t, byName(t, ss, "ner").Complete())
		})
	}
}

func TestService_List_Mismatch(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.svc.Pull(context.Background(), []string{"ner"}, app.PullOptions{})
	require.NoError(t, err)
	p := filepath.Join(r.dir, "ner", "r3", "onnx", "model.onnx")
	require.NoError(t, os.WriteFile(p, []byte("tampered"), 0o600))
	ss, err := r.svc.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, app.FileMismatch, byName(t, ss, "ner").Files[0].State)
}

type errVerifier struct{ err error }

func (v errVerifier) Verify(string, dom.File) error { return v.err }

func TestService_List_Errors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	t.Run("load", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.store.loadErr = boom
		_, err := r.svc.List(context.Background())
		require.ErrorIs(t, err, boom)
	})
	t.Run("verify", func(t *testing.T) {
		t.Parallel()
		svc, err := app.New(testManifest(), &fakeFetcher{}, nil, &memStore{}, t.TempDir(),
			app.WithVerifier(errVerifier{boom}))
		require.NoError(t, err)
		_, err = svc.List(context.Background())
		require.ErrorIs(t, err, boom)
		err = svc.Require(context.Background(), "bert")
		require.ErrorIs(t, err, boom)
	})
}

func TestService_Require_Scenarios(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("missing files carry the hint", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		err := r.svc.Require(ctx, "bert")
		require.ErrorIs(t, err, dom.ErrMissing)
		assert.ErrorContains(t, err, `run "triage models pull bert"`)
	})
	t.Run("pulled without int8 satisfies", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.svc.Pull(ctx, []string{"all"}, app.PullOptions{})
		require.NoError(t, err)
		for _, n := range []string{"bert", "mf", "ner"} {
			require.NoError(t, r.svc.Require(ctx, n))
		}
	})
	t.Run("derived missing", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.svc.Pull(ctx, []string{"mf"}, app.PullOptions{})
		require.NoError(t, err)
		r.store.m = dom.Manifest{}
		err = r.svc.Require(ctx, "mf")
		require.ErrorIs(t, err, dom.ErrMissing)
		assert.ErrorContains(t, err, `run "triage models pull mf"`)
	})
	t.Run("checksum mismatch", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.svc.Pull(ctx, []string{"ner"}, app.PullOptions{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(r.dir, "ner", "r3", "onnx", "model.onnx"), []byte("x"), 0o600))
		err = r.svc.Require(ctx, "ner")
		require.ErrorIs(t, err, dom.ErrChecksum)
		assert.ErrorContains(t, err, `run "triage models pull ner"`)
	})
	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		require.ErrorIs(t, newRig(t).svc.Require(ctx, "x"), app.ErrUnknownName)
	})
	t.Run("load error", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.store.loadErr = errors.New("boom")
		require.Error(t, r.svc.Require(ctx, "ner"))
	})
}
