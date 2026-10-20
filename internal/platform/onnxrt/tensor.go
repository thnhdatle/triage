// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"fmt"
	"slices"
)

// DType is a tensor element type. The values are the ONNX
// TensorProto.DataType numbers.
type DType int

// Supported element types.
const (
	Float32 DType = 1
	Int64   DType = 7
)

// String returns the ONNX name of the type.
func (d DType) String() string {
	switch d {
	case Float32:
		return "float32"
	case Int64:
		return "int64"
	}
	return fmt.Sprintf("dtype(%d)", int(d))
}

// Tensor is a named dense tensor of float32 or int64 held in Go memory. The
// zero value is invalid; build one with Float32Tensor or Int64Tensor.
type Tensor struct {
	name  string
	shape []int64
	dtype DType
	f32   []float32
	i64   []int64
}

// Float32Tensor returns a float32 tensor. data is used in row-major order
// and is not copied.
func Float32Tensor(name string, shape []int64, data []float32) Tensor {
	return Tensor{name: name, shape: slices.Clone(shape), dtype: Float32, f32: data}
}

// Int64Tensor returns an int64 tensor (e.g. token ids). data is used in
// row-major order and is not copied.
func Int64Tensor(name string, shape []int64, data []int64) Tensor {
	return Tensor{name: name, shape: slices.Clone(shape), dtype: Int64, i64: data}
}

// Name returns the model input or output name.
func (t Tensor) Name() string { return t.name }

// Shape returns a copy of the dimensions.
func (t Tensor) Shape() []int64 { return slices.Clone(t.shape) }

// DType returns the element type.
func (t Tensor) DType() DType { return t.dtype }

// Float32Data returns the elements of a float32 tensor.
func (t Tensor) Float32Data() ([]float32, error) {
	if t.dtype != Float32 {
		return nil, fmt.Errorf("%w: %q is %s, not float32", ErrDType, t.name, t.dtype)
	}
	return t.f32, nil
}

// Int64Data returns the elements of an int64 tensor.
func (t Tensor) Int64Data() ([]int64, error) {
	if t.dtype != Int64 {
		return nil, fmt.Errorf("%w: %q is %s, not int64", ErrDType, t.name, t.dtype)
	}
	return t.i64, nil
}

// validate checks that the shape is positive and matches the data length.
func (t Tensor) validate() error {
	n := int64(1)
	for _, d := range t.shape {
		if d <= 0 {
			return fmt.Errorf("%w: %q has non-positive dimension in shape %v", ErrInvalidInput, t.name, t.shape)
		}
		n *= d
	}
	var have int
	switch t.dtype {
	case Float32:
		have = len(t.f32)
	case Int64:
		have = len(t.i64)
	default:
		return fmt.Errorf("%w: %q has %s", ErrUnsupportedType, t.name, t.dtype)
	}
	if int64(have) != n {
		return fmt.Errorf("%w: %q has %d elements, shape %v needs %d", ErrInvalidInput, t.name, have, t.shape, n)
	}
	return nil
}
