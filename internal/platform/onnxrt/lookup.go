// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// EnvLibraryPath is the environment variable consulted after the configured
// onnx.library_path.
const EnvLibraryPath = "TRIAGE_ONNX_LIBRARY_PATH"

// MinVersion is the oldest ONNX Runtime release whose C API (v29) the
// binding requires.
const MinVersion = "1.29"

const (
	minMajor = 1
	minMinor = 29
)

// defaultSystemDirs are the Homebrew prefixes (Apple silicon, Intel) searched last.
var defaultSystemDirs = []string{"/opt/homebrew/lib", "/usr/local/lib"}

// Option configures Open.
type Option func(*options)

type options struct {
	libraryPath string
	getenv      func(string) string
	executable  func() (string, error)
	systemDirs  []string
	minMajor    int
	minMinor    int
}

func newOptions(opts []Option) options {
	o := options{
		getenv:     os.Getenv,
		executable: os.Executable,
		systemDirs: defaultSystemDirs,
		minMajor:   minMajor,
		minMinor:   minMinor,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithLibraryPath sets onnx.library_path, the first candidate. A directory
// is searched for the platform's library file name; empty means unset.
func WithLibraryPath(path string) Option {
	return func(o *options) { o.libraryPath = path }
}

// WithGetenv replaces os.Getenv for reading TRIAGE_ONNX_LIBRARY_PATH.
func WithGetenv(getenv func(string) string) Option {
	return func(o *options) { o.getenv = getenv }
}

// WithExecutable replaces os.Executable for locating the binary's directory.
func WithExecutable(executable func() (string, error)) Option {
	return func(o *options) { o.executable = executable }
}

// WithSystemDirs replaces the Homebrew directories searched last
// (default /opt/homebrew/lib, /usr/local/lib).
func WithSystemDirs(dirs ...string) Option {
	return func(o *options) { o.systemDirs = dirs }
}

// LibraryName is the ONNX Runtime shared library file name on this platform.
func LibraryName() string {
	return libraryName(runtime.GOOS)
}

func libraryName(goos string) string {
	if goos == "darwin" {
		return "libonnxruntime.dylib"
	}
	return "libonnxruntime.so"
}

// Candidates returns the library paths Open tries, in order:
// onnx.library_path, TRIAGE_ONNX_LIBRARY_PATH, the binary's directory, then
// the Homebrew directories. Duplicates are removed.
func Candidates(opts ...Option) []string {
	return newOptions(opts).candidates()
}

func (o options) candidates() []string {
	name := LibraryName()
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(fileOrDir(o.libraryPath, name))
	add(fileOrDir(o.getenv(EnvLibraryPath), name))
	if exe, err := o.executable(); err == nil && exe != "" {
		add(filepath.Join(filepath.Dir(exe), name))
	}
	for _, d := range o.systemDirs {
		if d != "" {
			add(filepath.Join(d, name))
		}
	}
	return out
}

// fileOrDir returns p, or p/name when p is an existing directory.
func fileOrDir(p, name string) string {
	if p == "" {
		return ""
	}
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return filepath.Join(p, name)
	}
	return p
}

// versionAtLeast reports whether a "major.minor[.patch]" version is at least
// major.minor; an unparsable version is not.
func versionAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	minr, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return maj > major || (maj == major && minr >= minor)
}
