// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package onnxrt

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testIDs = []int64{1, 2, 3, 4, 0, 0, 0, 0}
	testX   = []float32{0.5, 0.5, 0.5, 0.5, 1, 2, 30, -10}
)

func addInputs() []Tensor {
	return []Tensor{
		Int64Tensor("ids", []int64{2, 4}, testIDs),
		Float32Tensor("x", []int64{2, 4}, testX),
	}
}

func newRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := Open()
	require.NoError(t, err, "ONNX Runtime >= %s is required: run make bootstrap or set %s", MinVersion, EnvLibraryPath)
	t.Cleanup(func() { assert.NoError(t, rt.Close()) })
	return rt
}

func newSession(t *testing.T, rt *Runtime, model []byte, cfg SessionConfig) *Session {
	t.Helper()
	s, err := rt.NewSession(model, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, s.Close()) })
	return s
}

func assertAddOutputs(t *testing.T, out []Tensor) {
	t.Helper()
	wantLogits, wantTop := addReference(testIDs, testX)
	require.Len(t, out, 2)
	assert.Equal(t, "logits", out[0].Name())
	assert.Equal(t, []int64{2, 3}, out[0].Shape())
	logits, err := out[0].Float32Data()
	require.NoError(t, err)
	assert.InDeltaSlice(t, wantLogits, logits, 1e-6)
	assert.Equal(t, "top", out[1].Name())
	top, err := out[1].Int64Data()
	require.NoError(t, err)
	assert.Equal(t, wantTop, top)
	assert.Equal(t, []int64{0, 2}, top, "the reference picks distinct classes per row")
}

func TestSessionRun(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	s := newSession(t, rt, addModel(), SessionConfig{IntraOpThreads: 2, InterOpThreads: 1})
	assert.Equal(t, []string{"ids", "x"}, s.InputNames(), "names are discovered from the model")
	assert.Equal(t, []string{"logits", "top"}, s.OutputNames())

	out, err := s.Run(t.Context(), addInputs()...)
	require.NoError(t, err)
	assertAddOutputs(t, out)

	in := addInputs()
	out, err = s.Run(t.Context(), in[1], in[0])
	require.NoError(t, err, "inputs are matched by name")
	assertAddOutputs(t, out)
}

func TestSessionFromFileWithNamedOutputs(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	path := filepath.Join(t.TempDir(), "add.onnx")
	require.NoError(t, os.WriteFile(path, addModel(), 0o600))
	s, err := rt.NewSessionFromFile(path, SessionConfig{OutputNames: []string{"top"}})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()
	assert.Equal(t, []string{"ids", "x"}, s.InputNames())

	out, err := s.Run(t.Context(), addInputs()...)
	require.NoError(t, err)
	require.Len(t, out, 1)
	top, err := out[0].Int64Data()
	require.NoError(t, err)
	assert.Equal(t, []int64{0, 2}, top)
}

func TestSessionCreateErrors(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	missing := filepath.Join(t.TempDir(), "missing.onnx")
	tests := []struct {
		name    string
		create  func() (*Session, error)
		wantErr error
		wantMsg string
	}{
		{
			name:    "negative intra-op threads",
			create:  func() (*Session, error) { return rt.NewSession(addModel(), SessionConfig{IntraOpThreads: -1}) },
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "negative inter-op threads",
			create:  func() (*Session, error) { return rt.NewSession(addModel(), SessionConfig{InterOpThreads: -1}) },
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "garbage bytes, names discovered",
			create:  func() (*Session, error) { return rt.NewSession([]byte("garbage"), SessionConfig{}) },
			wantMsg: "read inputs and outputs of model bytes",
		},
		{
			name: "garbage bytes, names given",
			create: func() (*Session, error) {
				return rt.NewSession([]byte("garbage"), SessionConfig{InputNames: []string{"a"}, OutputNames: []string{"b"}})
			},
			wantMsg: "create session from model bytes",
		},
		{
			name:    "missing file",
			create:  func() (*Session, error) { return rt.NewSessionFromFile(missing, SessionConfig{}) },
			wantMsg: missing,
		},
		{
			name: "unknown output name",
			create: func() (*Session, error) {
				return rt.NewSession(addModel(), SessionConfig{OutputNames: []string{"nope"}})
			},
			wantMsg: "nope",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := tc.create()
			if err == nil {
				defer func() { assert.NoError(t, s.Close()) }()
				_, err = s.Run(t.Context(), addInputs()...)
			}
			require.Error(t, err)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestSessionRunInvalidInput(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	s := newSession(t, rt, addModel(), SessionConfig{})
	ids := Int64Tensor("ids", []int64{2, 4}, testIDs)
	tests := []struct {
		name    string
		in      []Tensor
		wantErr error
	}{
		{name: "missing input", in: []Tensor{ids}, wantErr: ErrInvalidInput},
		{name: "unknown name", in: []Tensor{ids, Float32Tensor("y", []int64{2, 4}, testX)}, wantErr: ErrInvalidInput},
		{name: "duplicate", in: []Tensor{ids, ids}, wantErr: ErrInvalidInput},
		{name: "shape mismatch", in: []Tensor{ids, Float32Tensor("x", []int64{3, 4}, testX)}, wantErr: ErrInvalidInput},
		{name: "wrong element type", in: []Tensor{ids, Int64Tensor("x", []int64{2, 4}, testIDs)}},
		{name: "wrong model dimension", in: []Tensor{
			Int64Tensor("ids", []int64{4, 2}, testIDs), Float32Tensor("x", []int64{4, 2}, testX),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := s.Run(t.Context(), tc.in...)
			require.Error(t, err)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestSessionUnsupportedOutput(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	s := newSession(t, rt, castModel(onnxInt32), SessionConfig{})
	_, err := s.Run(t.Context(), Float32Tensor("x", []int64{1}, []float32{3}))
	require.ErrorIs(t, err, ErrUnsupportedType)

	s64 := newSession(t, rt, castModel(onnxInt64), SessionConfig{})
	out, err := s64.Run(t.Context(), Float32Tensor("x", []int64{1}, []float32{3}))
	require.NoError(t, err)
	v, err := out[0].Int64Data()
	require.NoError(t, err)
	assert.Equal(t, []int64{3}, v)
}

func TestSessionClose(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	s, err := rt.NewSession(addModel(), SessionConfig{})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "Close is idempotent")
	_, err = s.Run(t.Context(), addInputs()...)
	require.ErrorIs(t, err, ErrClosed)
}

func TestSessionRunContext(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)

	t.Run("already cancelled", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, rt, addModel(), SessionConfig{})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := s.Run(ctx, addInputs()...)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("deadline during the run terminates it", func(t *testing.T) {
		t.Parallel()
		const n, k = 512, 400
		s := newSession(t, rt, slowModel(n, k), SessionConfig{IntraOpThreads: 1})
		x := make([]float32, n*n)
		for i := range x {
			x[i] = 1.0 / n
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := s.Run(ctx, Float32Tensor("x", []int64{n, n}, x))
		require.ErrorIs(t, err, context.DeadlineExceeded,
			"the run must be terminated, not completed (%d MatMuls of %dx%d)", k, n, n)
		t.Logf("terminated after %v", time.Since(start))
	})
}

func TestSessionConcurrentRuns(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	s := newSession(t, rt, addModel(), SessionConfig{IntraOpThreads: 1})
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			out, err := s.Run(t.Context(), addInputs()...)
			if err == nil {
				_, err = out[0].Float32Data()
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

func TestPoolRun(t *testing.T) {
	t.Parallel()
	rt := newRuntime(t)
	s := newSession(t, rt, addModel(), SessionConfig{IntraOpThreads: 1})
	p, err := NewPool(s, PoolConfig{MaxConcurrency: 2, QueueSize: 64})
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Go(func() {
			out, err := p.Run(t.Context(), addInputs()...)
			if err == nil && len(out) != 2 {
				err = assert.AnError
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "32 callers fit in 2 slots + 64 queue places")
	}

	l, err := p.Acquire(t.Context())
	require.NoError(t, err)
	out, err := l.Run(t.Context(), addInputs()...)
	l.Release()
	require.NoError(t, err)
	assertAddOutputs(t, out)
	require.NoError(t, p.Close(t.Context()))
	_, err = p.Run(t.Context(), addInputs()...)
	require.ErrorIs(t, err, ErrPoolClosed)
}
