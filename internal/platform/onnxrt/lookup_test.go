// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCandidatesOrder(t *testing.T) {
	t.Parallel()
	name := LibraryName()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "custom", "libort.so")
	tests := []struct {
		name string
		opts []Option
		want []string
	}{
		{
			name: "all sources in lookup order",
			opts: []Option{
				WithLibraryPath("/cfg/libonnxruntime.x"),
				WithGetenv(func(k string) string { return map[string]string{EnvLibraryPath: envFile}[k] }),
				WithExecutable(func() (string, error) { return "/app/bin/triage", nil }),
				WithSystemDirs("/opt/homebrew/lib", "", "/usr/local/lib"),
			},
			want: []string{
				"/cfg/libonnxruntime.x", envFile, filepath.Join("/app/bin", name),
				filepath.Join("/opt/homebrew/lib", name), filepath.Join("/usr/local/lib", name),
			},
		},
		{
			name: "directory configured, duplicates and unset sources dropped",
			opts: []Option{
				WithLibraryPath(dir),
				WithGetenv(func(string) string { return "" }),
				WithExecutable(func() (string, error) { return "", errors.New("no executable") }),
				WithSystemDirs(dir),
			},
			want: []string{filepath.Join(dir, name)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, Candidates(tc.opts...))
		})
	}
}

func TestCandidatesDefaults(t *testing.T) {
	t.Parallel()
	c := Candidates(WithGetenv(func(string) string { return "" }))
	require.GreaterOrEqual(t, len(c), 3)
	exe, err := os.Executable()
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(filepath.Dir(exe), LibraryName()),
		filepath.Join("/opt/homebrew/lib", LibraryName()),
		filepath.Join("/usr/local/lib", LibraryName()),
	}, c)
}

func TestLibraryName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "libonnxruntime.dylib", libraryName("darwin"))
	assert.Equal(t, "libonnxruntime.so", libraryName("linux"))
}

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    string
		want bool
	}{
		{name: "exact minimum", v: "1.29.0", want: true},
		{name: "newer minor", v: "1.31.0", want: true},
		{name: "newer major", v: "2.0", want: true},
		{name: "older minor", v: "1.28.9", want: false},
		{name: "older major", v: "0.99.0", want: false},
		{name: "major only", v: "1", want: false},
		{name: "garbage major", v: "x.30", want: false},
		{name: "garbage minor", v: "1.y", want: false},
		{name: "empty", v: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, versionAtLeast(tc.v, 1, 29))
		})
	}
}

func TestLookupError(t *testing.T) {
	t.Parallel()
	err := error(&LookupError{Tried: []Attempt{
		{Path: "/a/libonnxruntime.so", Err: errors.New("not found")},
		{Path: "/b/libonnxruntime.so", Err: errors.New("version 1.20.0 is older than 1.29")},
	}})
	require.ErrorIs(t, err, ErrLibraryNotFound)
	assert.Equal(t, "onnxrt: no usable ONNX Runtime library (minimum version 1.29); tried: "+
		"/a/libonnxruntime.so (not found); /b/libonnxruntime.so (version 1.20.0 is older than 1.29)", err.Error())
	assert.Equal(t, "onnxrt: no usable ONNX Runtime library (minimum version 1.29); tried: no candidate paths",
		(&LookupError{}).Error())
}

func TestRejections(t *testing.T) {
	t.Parallel()
	for _, err := range []error{ErrQueueFull, ErrPoolClosed} {
		var rej interface{ Rejected() bool }
		require.ErrorAs(t, err, &rej)
		assert.True(t, rej.Rejected())
	}
	assert.Equal(t, "onnxrt: scoring queue full", ErrQueueFull.Error())
}
