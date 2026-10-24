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

var env struct {
	mu      sync.Mutex
	refs    int
	path    string
	version string
}

// Runtime is a reference to the loaded ONNX Runtime.
type Runtime struct {
	path    string
	version string

	mu       sync.Mutex
	closed   bool
	sessions map[*Session]struct{}
}

// Open loads ONNX Runtime on first use and returns a reference to it.
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

func (r *Runtime) LibraryPath() string { return r.path }

func (r *Runtime) Version() string { return r.version }

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

func (r *Runtime) forget(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, s)
}
