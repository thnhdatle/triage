// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"errors"
	"strings"
)

var (
	ErrLibraryNotFound = errors.New("onnxrt: ONNX Runtime library not found")

	ErrNotBuilt = errors.New("onnxrt: ONNX Runtime support not built (binary built with -tags noonnx or CGO_ENABLED=0)")

	ErrClosed          = errors.New("onnxrt: closed")
	ErrInvalidConfig   = errors.New("onnxrt: invalid config")
	ErrInvalidInput    = errors.New("onnxrt: invalid input")
	ErrUnsupportedType = errors.New("onnxrt: unsupported tensor type")
	ErrDType           = errors.New("onnxrt: tensor element type mismatch")

	ErrQueueFull  error = &rejection{msg: "onnxrt: scoring queue full"}
	ErrPoolClosed error = &rejection{msg: "onnxrt: pool closed"}
)

type rejection struct{ msg string }

func (e *rejection) Error() string { return e.msg }

func (*rejection) Rejected() bool { return true }

type Attempt struct {
	Path string
	Err  error
}

type LookupError struct {
	Tried []Attempt
}

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

func (e *LookupError) Is(target error) bool { return target == ErrLibraryNotFound }
