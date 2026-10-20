// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTensorAccessors(t *testing.T) {
	t.Parallel()
	shape := []int64{1, 2}
	f := Float32Tensor("x", shape, []float32{1, 2})
	shape[0] = 9 // the tensor keeps its own copy
	assert.Equal(t, "x", f.Name())
	assert.Equal(t, []int64{1, 2}, f.Shape())
	assert.Equal(t, Float32, f.DType())
	fd, err := f.Float32Data()
	require.NoError(t, err)
	assert.Equal(t, []float32{1, 2}, fd)
	_, err = f.Int64Data()
	require.ErrorIs(t, err, ErrDType)

	i := Int64Tensor("ids", []int64{2}, []int64{3, 4})
	assert.Equal(t, Int64, i.DType())
	id, err := i.Int64Data()
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 4}, id)
	_, err = i.Float32Data()
	require.ErrorIs(t, err, ErrDType)
}

func TestDTypeString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "float32", Float32.String())
	assert.Equal(t, "int64", Int64.String())
	assert.Equal(t, "dtype(6)", DType(6).String())
}

func TestTensorValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		t       Tensor
		wantErr error
	}{
		{name: "float ok", t: Float32Tensor("x", []int64{2, 2}, make([]float32, 4))},
		{name: "int ok", t: Int64Tensor("x", []int64{3}, make([]int64, 3))},
		{name: "scalar ok", t: Int64Tensor("x", nil, []int64{1})},
		{name: "zero dim", t: Int64Tensor("x", []int64{0, 2}, nil), wantErr: ErrInvalidInput},
		{name: "negative dim", t: Float32Tensor("x", []int64{-1}, nil), wantErr: ErrInvalidInput},
		{name: "too few elements", t: Float32Tensor("x", []int64{2, 2}, make([]float32, 3)), wantErr: ErrInvalidInput},
		{name: "too many elements", t: Int64Tensor("x", []int64{1}, make([]int64, 2)), wantErr: ErrInvalidInput},
		{name: "zero value", t: Tensor{}, wantErr: ErrUnsupportedType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.t.validate()
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}
