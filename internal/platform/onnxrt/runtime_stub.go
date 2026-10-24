// SPDX-License-Identifier: Apache-2.0

//go:build !cgo || noonnx

package onnxrt

import "context"

// Runtime is a reference to the loaded ONNX Runtime.
type Runtime struct{}

// Open always fails with ErrNotBuilt in this build.
func Open(...Option) (*Runtime, error) { return nil, ErrNotBuilt }

func (*Runtime) LibraryPath() string { return "" }

func (*Runtime) Version() string { return "" }

func (*Runtime) Close() error { return nil }

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

func (*Runtime) NewSessionFromFile(string, SessionConfig) (*Session, error) { return nil, ErrNotBuilt }

func (*Session) InputNames() []string { return nil }

func (*Session) OutputNames() []string { return nil }

// Run feeds the input tensors and returns the outputs in OutputNames order.
func (*Session) Run(context.Context, ...Tensor) ([]Tensor, error) { return nil, ErrNotBuilt }

func (*Session) Close() error { return nil }
