// SPDX-License-Identifier: Apache-2.0

//go:build !cgo || noonnx

package onnxrt

import "context"

// Runtime is a reference to the loaded ONNX Runtime environment. In this
// build (-tags noonnx or CGO_ENABLED=0) it cannot be created: Open always
// fails with ErrNotBuilt.
type Runtime struct{}

// Open always fails with ErrNotBuilt in this build.
func Open(...Option) (*Runtime, error) { return nil, ErrNotBuilt }

// LibraryPath returns "" in this build.
func (*Runtime) LibraryPath() string { return "" }

// Version returns "" in this build.
func (*Runtime) Version() string { return "" }

// Close does nothing in this build.
func (*Runtime) Close() error { return nil }

// SessionConfig configures a session (see the cgo build).
type SessionConfig struct {
	IntraOpThreads int
	InterOpThreads int
	InputNames     []string
	OutputNames    []string
}

// Session is a loaded model; it cannot be created in this build.
type Session struct{}

// NewSession fails with ErrNotBuilt in this build.
func (*Runtime) NewSession([]byte, SessionConfig) (*Session, error) { return nil, ErrNotBuilt }

// NewSessionFromFile fails with ErrNotBuilt in this build.
func (*Runtime) NewSessionFromFile(string, SessionConfig) (*Session, error) { return nil, ErrNotBuilt }

// InputNames returns nil in this build.
func (*Session) InputNames() []string { return nil }

// OutputNames returns nil in this build.
func (*Session) OutputNames() []string { return nil }

// Run fails with ErrNotBuilt in this build.
func (*Session) Run(context.Context, ...Tensor) ([]Tensor, error) { return nil, ErrNotBuilt }

// Close does nothing in this build.
func (*Session) Close() error { return nil }
