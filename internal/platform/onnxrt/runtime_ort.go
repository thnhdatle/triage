// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package onnxrt

import (
	"errors"
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// env is the process-wide ONNX Runtime environment. onnxruntime_go keeps the
// loaded library and OrtEnv in package globals, so the reference count that
// decides when to load and unload them is process-wide too (the documented
// exception to the no-global-state rule).
var env struct {
	mu      sync.Mutex // guards every field
	refs    int
	path    string
	version string
}

// Runtime is a reference to the loaded ONNX Runtime environment. Create one
// in main with Open, pass it to the components that build sessions and close
// it at shutdown; the library is unloaded when the last Runtime is closed.
type Runtime struct {
	path    string
	version string

	mu       sync.Mutex // guards closed and sessions
	closed   bool
	sessions map[*Session]struct{}
}

// Open loads ONNX Runtime on first use and returns a reference to it. The
// library is looked up in a fixed order (see Candidates); a path
// that is missing, fails to load, or holds a release older than MinVersion
// is skipped, and if none is usable the *LookupError names every path tried.
// While another Runtime is open the loaded library is shared and opts are
// ignored.
func Open(opts ...Option) (*Runtime, error) {
	env.mu.Lock()
	defer env.mu.Unlock()
	if env.refs == 0 {
		path, version, err := load(newOptions(opts))
		if err != nil {
			return nil, err
		}
		env.path, env.version = path, version
	}
	env.refs++
	return &Runtime{path: env.path, version: env.version, sessions: map[*Session]struct{}{}}, nil
}

// load tries each candidate until one initialises an environment of a
// supported version. The caller holds env.mu.
func load(o options) (path, version string, err error) {
	lerr := &LookupError{}
	for _, c := range o.candidates() {
		if _, serr := os.Stat(c); serr != nil {
			lerr.Tried = append(lerr.Tried, Attempt{Path: c, Err: errors.New("not found")})
			continue
		}
		ort.SetSharedLibraryPath(c)
		if ierr := ort.InitializeEnvironment(ort.WithLogLevelError()); ierr != nil {
			lerr.Tried = append(lerr.Tried, Attempt{Path: c, Err: ierr})
			continue
		}
		v := ort.GetVersion()
		if !versionAtLeast(v, o.minMajor, o.minMinor) {
			lerr.Tried = append(lerr.Tried, Attempt{Path: c, Err: fmt.Errorf("version %s is older than %d.%d",
				v, o.minMajor, o.minMinor)})
			if derr := ort.DestroyEnvironment(); derr != nil {
				lerr.Tried[len(lerr.Tried)-1].Err = errors.Join(lerr.Tried[len(lerr.Tried)-1].Err, derr)
			}
			continue
		}
		return c, v, nil
	}
	return "", "", lerr
}

// LibraryPath returns the path the library was loaded from.
func (r *Runtime) LibraryPath() string { return r.path }

// Version returns the loaded ONNX Runtime version, e.g. "1.31.0".
func (r *Runtime) Version() string { return r.version }

// Close closes every session still open on r and drops r's reference; the
// last Close destroys the environment and unloads the library. It is safe to
// call more than once.
func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	open := make([]*Session, 0, len(r.sessions))
	for s := range r.sessions {
		open = append(open, s)
	}
	r.sessions = nil
	r.mu.Unlock()

	var errs []error
	for _, s := range open {
		errs = append(errs, s.Close())
	}
	env.mu.Lock()
	defer env.mu.Unlock()
	env.refs--
	if env.refs == 0 {
		env.path, env.version = "", ""
		if err := ort.DestroyEnvironment(); err != nil {
			errs = append(errs, fmt.Errorf("onnxrt: destroy environment: %w", err))
		}
	}
	return errors.Join(errs...)
}

// forget unregisters a closed session.
func (r *Runtime) forget(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, s)
}
