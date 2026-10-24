// SPDX-License-Identifier: Apache-2.0

package mf

import (
	"fmt"
	"math"
	"os"
)

const (
	TensorModels     = "P.weight"
	TensorClassifier = "classifier.0.weight"
	TensorProjection = "text_proj.0.weight"
	StrongRow        = 24
	WeakRow          = 36
	Dimensions       = 1536
	VectorTensor     = "u"
)

const normEps = 1e-12

func ComputeU(f *File, strong, weak int) ([]float64, error) {
	p, pShape, err := tensor(f, TensorModels, 2)
	if err != nil {
		return nil, err
	}
	dim := pShape[1]
	c, cShape, err := tensor(f, TensorClassifier, 2)
	if err != nil {
		return nil, err
	}
	w, wShape, err := tensor(f, TensorProjection, 2)
	if err != nil {
		return nil, err
	}
	switch {
	case cShape[0] != 1 || cShape[1] != dim:
		return nil, fmt.Errorf("%w: %s shape %v, want [1 %d]", ErrSafetensors, TensorClassifier, cShape, dim)
	case wShape[0] != dim || wShape[1] == 0:
		return nil, fmt.Errorf("%w: %s shape %v, want [%d D]", ErrSafetensors, TensorProjection, wShape, dim)
	case strong < 0 || weak < 0 || strong >= pShape[0] || weak >= pShape[0]:
		return nil, fmt.Errorf("%w: rows %d and %d outside %s %v", ErrSafetensors, strong, weak, TensorModels, pShape)
	}
	ps, pw := normalized(p[strong*dim:(strong+1)*dim]), normalized(p[weak*dim:(weak+1)*dim])
	out := wShape[1]
	u := make([]float64, out)
	for k := range dim {
		a := c[k] * (ps[k] - pw[k])
		row := w[k*out : (k+1)*out]
		for j, x := range row {
			u[j] += x * a
		}
	}
	return u, nil
}

func tensor(f *File, name string, rank int) ([]float64, []int, error) {
	t, ok := f.Tensors[name]
	if !ok {
		return nil, nil, fmt.Errorf("%w: tensor %q missing", ErrSafetensors, name)
	}
	if len(t.Shape) != rank {
		return nil, nil, fmt.Errorf("%w: tensor %q has shape %v, want rank %d", ErrSafetensors, name, t.Shape, rank)
	}
	v, err := t.Float64s()
	if err != nil {
		return nil, nil, fmt.Errorf("tensor %q: %w", name, err)
	}
	return v, t.Shape, nil
}

func normalized(v []float64) []float64 {
	var ss float64
	for _, x := range v {
		ss += x * x
	}
	n := math.Max(math.Sqrt(ss), normEps)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func LoadVector(path string) ([]float64, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path comes from the artifact cache.
	if err != nil {
		return nil, fmt.Errorf("mf: read vector: %w", err)
	}
	f, err := ParseSafetensors(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	u, shape, err := tensor(f, VectorTensor, 1)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if shape[0] == 0 {
		return nil, fmt.Errorf("%w: %s: empty vector", ErrSafetensors, path)
	}
	for _, x := range u {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("%w: %s: non-finite element", ErrSafetensors, path)
		}
	}
	return u, nil
}
