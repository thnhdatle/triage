// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const EnvLibraryPath = "TRIAGE_ONNX_LIBRARY_PATH"

const MinVersion = "1.29"

const (
	minMajor = 1
	minMinor = 29
)

var defaultSystemDirs = []string{"/opt/homebrew/lib", "/usr/local/lib"}

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

func WithLibraryPath(path string) Option {
	return func(o *options) { o.libraryPath = path }
}

func WithGetenv(getenv func(string) string) Option {
	return func(o *options) { o.getenv = getenv }
}

func WithExecutable(executable func() (string, error)) Option {
	return func(o *options) { o.executable = executable }
}

func WithSystemDirs(dirs ...string) Option {
	return func(o *options) { o.systemDirs = dirs }
}

func LibraryName() string {
	return libraryName(runtime.GOOS)
}

func libraryName(goos string) string {
	if goos == "darwin" {
		return "libonnxruntime.dylib"
	}
	return "libonnxruntime.so"
}

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

func fileOrDir(p, name string) string {
	if p == "" {
		return ""
	}
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return filepath.Join(p, name)
	}
	return p
}

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
