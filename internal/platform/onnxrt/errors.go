// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"errors"
	"strings"
)

// Errors of the package. ErrQueueFull and ErrPoolClosed implement the
// Rejected() bool method that internal/app/route classifies as cause
// "rejected".
var (
	// ErrLibraryNotFound is matched by the *LookupError Open returns when no
	// candidate path holds a usable ONNX Runtime library.
	ErrLibraryNotFound = errors.New("onnxrt: ONNX Runtime library not found")
	// ErrNotBuilt is returned by Open in a binary built with -tags noonnx or
	// without cgo.
	ErrNotBuilt = errors.New("onnxrt: ONNX Runtime support not built (binary built with -tags noonnx or CGO_ENABLED=0)")
	// ErrClosed is returned when a closed Runtime or Session is used.
	ErrClosed = errors.New("onnxrt: closed")
	// ErrInvalidConfig is returned for an invalid SessionConfig or PoolConfig.
	ErrInvalidConfig = errors.New("onnxrt: invalid config")
	// ErrInvalidInput is returned by Run for a tensor whose name, shape or
	// data do not match the session's inputs.
	ErrInvalidInput = errors.New("onnxrt: invalid input")
	// ErrUnsupportedType is returned for a model output whose element type is
	// neither float32 nor int64.
	ErrUnsupportedType = errors.New("onnxrt: unsupported tensor type")
	// ErrDType is returned by a Tensor accessor of the wrong element type.
	ErrDType = errors.New("onnxrt: tensor element type mismatch")
	// ErrQueueFull is returned by Pool.Acquire when every run slot is busy
	// and the wait queue is full.
	ErrQueueFull error = &rejection{msg: "onnxrt: scoring queue full"}
	// ErrPoolClosed is returned by Pool.Acquire once Close has been called.
	ErrPoolClosed error = &rejection{msg: "onnxrt: pool closed"}
)

// rejection is an error that means the pool refused work without trying.
type rejection struct{ msg string }

func (e *rejection) Error() string { return e.msg }

// Rejected reports that the request was refused without being run.
func (*rejection) Rejected() bool { return true }

// Attempt is one candidate path tried while loading the library.
type Attempt struct {
	Path string
	Err  error
}

// LookupError lists every path tried and why each failed. It
// matches ErrLibraryNotFound with errors.Is.
type LookupError struct {
	Tried []Attempt
}

// Error names every path tried and the minimum supported version.
func (e *LookupError) Error() string {
	var b strings.Builder
	b.WriteString("onnxrt: no usable ONNX Runtime library (minimum version ")
	b.WriteString(MinVersion)
	b.WriteString("); tried:")
	if len(e.Tried) == 0 {
		b.WriteString(" no candidate paths")
	}
	for i, a := range e.Tried {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteByte(' ')
		b.WriteString(a.Path)
		b.WriteString(" (")
		b.WriteString(a.Err.Error())
		b.WriteByte(')')
	}
	return b.String()
}

// Is matches ErrLibraryNotFound.
func (e *LookupError) Is(target error) bool { return target == ErrLibraryNotFound }
