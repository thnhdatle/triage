// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package onnxrt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file are not parallel: they observe the process-wide
// environment (load, reference count, unload), which parallel tests holding a
// Runtime would keep loaded. Go runs every top-level parallel test after the
// sequential ones, so each of these starts with the library unloaded.

// withMinVersion overrides the minimum accepted version.
func withMinVersion(major, minor int) Option {
	return func(o *options) { o.minMajor, o.minMinor = major, minor }
}

// realLibrary returns the path the default lookup would load.
func realLibrary(t *testing.T) string {
	t.Helper()
	for _, c := range Candidates() {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Fatalf("ONNX Runtime >= %s not found in %v: run make bootstrap or set %s", MinVersion, Candidates(), EnvLibraryPath)
	return ""
}

func envRefs() int {
	env.mu.Lock()
	defer env.mu.Unlock()
	return env.refs
}

// noEnv isolates a lookup from TRIAGE_ONNX_LIBRARY_PATH and the binary's directory.
func noEnv() []Option {
	return []Option{
		WithGetenv(func(string) string { return "" }),
		WithExecutable(func() (string, error) { return "", errors.New("unknown") }),
	}
}

//nolint:paralleltest // observes the process-wide ORT environment (see file comment).
func TestOpenReferenceCounting(t *testing.T) {
	require.Equal(t, 0, envRefs())
	r1, err := Open()
	require.NoError(t, err)
	assert.True(t, versionAtLeast(r1.Version(), 1, 29), "version %s", r1.Version())
	assert.Equal(t, realLibrary(t), r1.LibraryPath())

	r2, err := Open(WithLibraryPath("/ignored/while/loaded"))
	require.NoError(t, err, "a second Open shares the loaded library")
	assert.Equal(t, r1.LibraryPath(), r2.LibraryPath())
	assert.Equal(t, 2, envRefs())

	require.NoError(t, r1.Close())
	require.NoError(t, r1.Close(), "Close is idempotent")
	assert.Equal(t, 1, envRefs())
	s, err := r2.NewSession(addModel(), SessionConfig{})
	require.NoError(t, err, "the environment survives while a reference remains")
	_, err = s.Run(t.Context(), addInputs()...)
	require.NoError(t, err)

	require.NoError(t, r2.Close())
	assert.Equal(t, 0, envRefs(), "the last Close unloads the library")

	r3, err := Open()
	require.NoError(t, err, "the library loads again after unloading")
	require.NoError(t, r3.Close())
}

//nolint:paralleltest // observes the process-wide ORT environment (see file comment).
func TestOpenNotFound(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, LibraryName())
	require.NoError(t, os.WriteFile(bogus, []byte("not a shared library"), 0o600))
	missing := filepath.Join(dir, "missing", "libonnxruntime.so")
	empty := t.TempDir()

	_, err := Open(
		WithLibraryPath(bogus),
		WithGetenv(func(string) string { return missing }),
		WithExecutable(func() (string, error) { return "", errors.New("unknown") }),
		WithSystemDirs(empty),
	)
	require.ErrorIs(t, err, ErrLibraryNotFound)
	var lerr *LookupError
	require.ErrorAs(t, err, &lerr)
	require.Len(t, lerr.Tried, 3)
	assert.Equal(t, []string{bogus, missing, filepath.Join(empty, LibraryName())},
		[]string{lerr.Tried[0].Path, lerr.Tried[1].Path, lerr.Tried[2].Path})
	for _, p := range []string{bogus, missing, empty, "minimum version 1.29"} {
		assert.Contains(t, err.Error(), p)
	}
	assert.Equal(t, 0, envRefs())
}

//nolint:paralleltest // observes the process-wide ORT environment (see file comment).
func TestOpenRejectsOldVersion(t *testing.T) {
	lib := realLibrary(t)
	_, err := Open(append(noEnv(), WithLibraryPath(lib), WithSystemDirs(), withMinVersion(99, 0))...)
	require.ErrorIs(t, err, ErrLibraryNotFound)
	assert.Contains(t, err.Error(), "is older than 99.0")
	assert.Equal(t, 0, envRefs(), "a rejected library is unloaded")
}

//nolint:paralleltest // observes the process-wide ORT environment (see file comment).
func TestOpenFallsThrough(t *testing.T) {
	lib := realLibrary(t)
	missing := filepath.Join(t.TempDir(), LibraryName())
	rt, err := Open(
		WithLibraryPath(missing),
		WithGetenv(func(k string) string { return map[string]string{EnvLibraryPath: lib}[k] }),
		WithSystemDirs(),
	)
	require.NoError(t, err)
	assert.Equal(t, lib, rt.LibraryPath(), "TRIAGE_ONNX_LIBRARY_PATH is used when onnx.library_path is missing")
	require.NoError(t, rt.Close())
}

//nolint:paralleltest // observes the process-wide ORT environment (see file comment).
func TestRuntimeCloseClosesSessions(t *testing.T) {
	rt, err := Open()
	require.NoError(t, err)
	s, err := rt.NewSession(addModel(), SessionConfig{})
	require.NoError(t, err)
	require.NoError(t, rt.Close())

	_, err = s.Run(t.Context(), addInputs()...)
	require.ErrorIs(t, err, ErrClosed)
	require.NoError(t, s.Close(), "closing an already released session is a no-op")
	_, err = rt.NewSession(addModel(), SessionConfig{})
	require.ErrorIs(t, err, ErrClosed)
	assert.Equal(t, 0, envRefs())
}
