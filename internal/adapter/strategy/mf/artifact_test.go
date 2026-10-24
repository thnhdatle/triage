// SPDX-License-Identifier: Apache-2.0

package mf_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/domain/artifact"
)

func entryWithWeights(t *testing.T, weights []byte) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, mf.WeightsFileName), weights, 0o600))
	return dir
}

func TestDeriver(t *testing.T) {
	t.Parallel()
	weights := readFile(t, filepath.Join(mfDir, mf.WeightsFileName))
	dir := entryWithWeights(t, weights)
	d := mf.Deriver{}
	assert.Equal(t, artifact.KindMFVector, d.Kind())
	got, err := d.Derive(t.Context(), artifact.Entry{Name: "mf"}, dir)
	require.NoError(t, err)
	assert.Equal(t, artifact.KindMFVector, got.Kind)
	assert.Equal(t, filepath.Join(dir, mf.VectorFileName), got.Path)
	b := readFile(t, got.Path)
	sum := sha256.Sum256(b)
	assert.Equal(t, hex.EncodeToString(sum[:]), got.SHA256)
	assert.Zero(t, binary.LittleEndian.Uint64(b)%8, "header padded to 8 bytes")

	f, err := mf.ParseSafetensors(b)
	require.NoError(t, err)
	ws := sha256.Sum256(weights)
	assert.Equal(t, hex.EncodeToString(ws[:]), f.Metadata["triage.source.sha256"])
	assert.Equal(t, "24", f.Metadata["triage.strong_row"])
	u, err := mf.LoadVector(got.Path)
	require.NoError(t, err)
	assert.Equal(t, fixtureU(t), u, "round trip is exact")

	again, err := d.Derive(t.Context(), artifact.Entry{}, dir)
	require.NoError(t, err)
	assert.Equal(t, got, again, "reproducible")
	left, err := filepath.Glob(filepath.Join(dir, "*.part"))
	require.NoError(t, err)
	assert.Empty(t, left)
}

func smallCheckpoint(t *testing.T, out int) []byte {
	t.Helper()
	p := make([]float32, 37*2)
	for i := range p {
		p[i] = float32(i%5) - 2
	}
	return buildST(t, map[string]st{
		mf.TensorModels:     {shape: []int{37, 2}, f32: p},
		mf.TensorClassifier: {shape: []int{1, 2}, f32: []float32{1, 1}},
		mf.TensorProjection: {shape: []int{2, out}, f32: make([]float32, 2*out)},
	}, nil)
}

func TestDeriverErrors(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	weights := readFile(t, filepath.Join(mfDir, mf.WeightsFileName))

	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		_, err := mf.Deriver{}.Derive(canceled, artifact.Entry{}, entryWithWeights(t, weights))
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("missing weights", func(t *testing.T) {
		t.Parallel()
		_, err := mf.Deriver{}.Derive(t.Context(), artifact.Entry{}, t.TempDir())
		require.ErrorIs(t, err, fs.ErrNotExist)
	})
	t.Run("not safetensors", func(t *testing.T) {
		t.Parallel()
		_, err := mf.Deriver{}.Derive(t.Context(), artifact.Entry{}, entryWithWeights(t, []byte("junk")))
		require.ErrorIs(t, err, mf.ErrSafetensors)
	})
	t.Run("missing tensor", func(t *testing.T) {
		t.Parallel()
		b := buildST(t, map[string]st{"x": {shape: []int{1}, f32: []float32{1}}}, nil)
		_, err := mf.Deriver{}.Derive(t.Context(), artifact.Entry{}, entryWithWeights(t, b))
		require.ErrorIs(t, err, mf.ErrSafetensors)
	})
	t.Run("wrong embedding size", func(t *testing.T) {
		t.Parallel()
		_, err := mf.Deriver{}.Derive(t.Context(), artifact.Entry{}, entryWithWeights(t, smallCheckpoint(t, 3)))
		require.ErrorIs(t, err, mf.ErrSafetensors)
	})
	t.Run("unwritable target", func(t *testing.T) {
		t.Parallel()
		dir := entryWithWeights(t, weights)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, mf.VectorFileName, "x"), 0o700))
		_, err := mf.Deriver{}.Derive(t.Context(), artifact.Entry{}, dir)
		require.Error(t, err)
		left, _ := filepath.Glob(filepath.Join(dir, "*.part"))
		assert.Empty(t, left)
	})
}

func TestComputeUErrors(t *testing.T) {
	t.Parallel()
	good := func() map[string]st {
		return map[string]st{
			mf.TensorModels:     {shape: []int{37, 2}, f32: make([]float32, 74)},
			mf.TensorClassifier: {shape: []int{1, 2}, f32: []float32{1, 1}},
			mf.TensorProjection: {shape: []int{2, 3}, f32: make([]float32, 6)},
		}
	}
	with := func(name string, v st) map[string]st {
		m := good()
		m[name] = v
		return m
	}
	without := func(name string) map[string]st {
		m := good()
		delete(m, name)
		return m
	}
	tests := []struct {
		name    string
		tensors map[string]st
		strong  int
		weak    int
	}{
		{name: "missing P", tensors: without(mf.TensorModels), strong: 24, weak: 36},
		{name: "missing classifier", tensors: without(mf.TensorClassifier), strong: 24, weak: 36},
		{name: "missing projection", tensors: without(mf.TensorProjection), strong: 24, weak: 36},
		{
			name: "P rank 1", tensors: with(mf.TensorModels, st{shape: []int{74}, f32: make([]float32, 74)}),
			strong: 24, weak: 36,
		},
		{name: "P int dtype", tensors: with(mf.TensorModels, st{
			dtype: "I32", shape: []int{37, 2},
			raw: make([]byte, 74*4),
		}), strong: 24, weak: 36},
		{
			name: "classifier shape", tensors: with(mf.TensorClassifier, st{shape: []int{2, 1}, f32: []float32{1, 1}}),
			strong: 24, weak: 36,
		},
		{
			name: "projection shape", tensors: with(mf.TensorProjection, st{shape: []int{3, 2}, f32: make([]float32, 6)}),
			strong: 24, weak: 36,
		},
		{
			name: "empty projection", tensors: with(mf.TensorProjection, st{shape: []int{2, 0}, raw: []byte{}}),
			strong: 24, weak: 36,
		},
		{name: "row out of range", tensors: good(), strong: 37, weak: 36},
		{name: "negative row", tensors: good(), strong: 24, weak: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := mf.ParseSafetensors(buildST(t, tc.tensors, nil))
			require.NoError(t, err)
			_, err = mf.ComputeU(f, tc.strong, tc.weak)
			require.ErrorIs(t, err, mf.ErrSafetensors)
		})
	}
}

func TestComputeUZeroRow(t *testing.T) {
	t.Parallel()
	p := make([]float32, 37*2)
	p[24*2], p[24*2+1] = 3, 4
	f, err := mf.ParseSafetensors(buildST(t, map[string]st{
		mf.TensorModels:     {shape: []int{37, 2}, f32: p},
		mf.TensorClassifier: {shape: []int{1, 2}, f32: []float32{2, 1}},
		mf.TensorProjection: {shape: []int{2, 2}, f32: []float32{1, 0, 0, 1}},
	}, nil))
	require.NoError(t, err)
	u, err := mf.ComputeU(f, mf.StrongRow, mf.WeakRow)
	require.NoError(t, err)
	assert.InDeltaSlice(t, []float64{2 * 0.6, 1 * 0.8}, u, 1e-7)
}

func TestLoadVectorErrors(t *testing.T) {
	t.Parallel()
	nan := binary.LittleEndian.AppendUint64(nil, math.Float64bits(math.NaN()))
	tests := []struct {
		name string
		data []byte
	}{
		{name: "not safetensors", data: []byte("x")},
		{name: "no u", data: buildST(t, map[string]st{"v": {shape: []int{1}, f32: []float32{1}}}, nil)},
		{name: "rank 2", data: buildST(t, map[string]st{"u": {shape: []int{1, 1}, f32: []float32{1}}}, nil)},
		{name: "empty", data: buildST(t, map[string]st{"u": {shape: []int{0}, raw: []byte{}}}, nil)},
		{name: "nan", data: buildST(t, map[string]st{"u": {dtype: "F64", shape: []int{1}, raw: nan}}, nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "v.safetensors")
			require.NoError(t, os.WriteFile(path, tc.data, 0o600))
			_, err := mf.LoadVector(path)
			require.ErrorIs(t, err, mf.ErrSafetensors)
		})
	}
	_, err := mf.LoadVector(filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestParseSafetensors(t *testing.T) {
	t.Parallel()
	ok := buildST(t, map[string]st{
		"a": {shape: []int{2}, f32: []float32{1.5, -2}},
		"b": {dtype: "BF16", shape: []int{2, 1}, raw: []byte{1, 2, 3, 4}},
		"s": {shape: []int{}, f32: []float32{7}},
	}, map[string]string{"k": "v"})
	f, err := mf.ParseSafetensors(ok)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"k": "v"}, f.Metadata)
	a, err := f.Tensors["a"].Float64s()
	require.NoError(t, err)
	assert.Equal(t, []float64{1.5, -2}, a)
	assert.Equal(t, []int{2, 1}, f.Tensors["b"].Shape)
	_, err = f.Tensors["b"].Float64s()
	require.ErrorIs(t, err, mf.ErrSafetensors)
	s, err := f.Tensors["s"].Float64s()
	require.NoError(t, err)
	assert.Equal(t, []float64{7}, s, "scalar")

	header := func(h string) []byte {
		return append(binary.LittleEndian.AppendUint64(nil, uint64(len(h))), h...)
	}
	bad := []struct {
		name string
		data []byte
	}{
		{name: "too short", data: []byte{1, 2}},
		{name: "header longer than file", data: binary.LittleEndian.AppendUint64(nil, 100)},
		{name: "header not json", data: header("{")},
		{name: "bad metadata", data: header(`{"__metadata__":{"k":1}}`)},
		{name: "bad entry", data: header(`{"a":[]}`)},
		{name: "unknown dtype", data: header(`{"a":{"dtype":"Q4","shape":[1],"data_offsets":[0,0]}}`)},
		{name: "offsets outside", data: header(`{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`)},
		{name: "reversed offsets", data: append(header(`{"a":{"dtype":"U8","shape":[1],"data_offsets":[1,0]}}`), 0)},
		{name: "negative dim", data: append(header(`{"a":{"dtype":"U8","shape":[-1],"data_offsets":[0,1]}}`), 0)},
		{name: "huge dim", data: append(header(`{"a":{"dtype":"F64","shape":[1,4611686018427387904],`+
			`"data_offsets":[0,1]}}`), 0)},
		{name: "size mismatch", data: append(header(`{"a":{"dtype":"F32","shape":[2],"data_offsets":[0,4]}}`),
			0, 0, 0, 0)},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := mf.ParseSafetensors(tc.data)
			require.ErrorIs(t, err, mf.ErrSafetensors)
		})
	}
}

func FuzzParseSafetensors(f *testing.F) {
	f.Add([]byte{})
	f.Add(binary.LittleEndian.AppendUint64(nil, 2))
	h := `{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,4]},"__metadata__":{"k":"v"}}`
	f.Add(append(append(binary.LittleEndian.AppendUint64(nil, uint64(len(h))), h...), 0, 0, 128, 63))
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := mf.ParseSafetensors(data)
		if err != nil {
			require.ErrorIs(t, err, mf.ErrSafetensors)
			return
		}
		for _, tn := range file.Tensors {
			if v, err := tn.Float64s(); err == nil {
				n := 1
				for _, d := range tn.Shape {
					n *= d
				}
				require.Len(t, v, n)
			}
		}
	})
}
