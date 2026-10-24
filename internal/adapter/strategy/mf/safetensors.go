// SPDX-License-Identifier: Apache-2.0

package mf

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

var ErrSafetensors = errors.New("mf: invalid safetensors")

const maxHeader = 100 << 20

const (
	dtypeF32 = "F32"
	dtypeF64 = "F64"
)

const metadataKey = "__metadata__"

var elemSize = map[string]int{
	"BOOL": 1, "U8": 1, "I8": 1, "F8_E4M3": 1, "F8_E5M2": 1,
	"I16": 2, "U16": 2, "F16": 2, "BF16": 2,
	"I32": 4, "U32": 4, dtypeF32: 4,
	"I64": 8, "U64": 8, dtypeF64: 8,
}

type Tensor struct {
	DType string
	Shape []int
	data  []byte
}

func (t Tensor) Float64s() ([]float64, error) {
	switch t.DType {
	case dtypeF32:
		out := make([]float64, len(t.data)/4)
		for i := range out {
			out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(t.data[4*i:])))
		}
		return out, nil
	case dtypeF64:
		out := make([]float64, len(t.data)/8)
		for i := range out {
			out[i] = math.Float64frombits(binary.LittleEndian.Uint64(t.data[8*i:]))
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: dtype %s is not F32 or F64", ErrSafetensors, t.DType)
}

type File struct {
	Metadata map[string]string
	Tensors  map[string]Tensor
}

type headerEntry struct {
	DType       string   `json:"dtype"`
	Shape       []int    `json:"shape"`
	DataOffsets [2]int64 `json:"data_offsets"`
}

func ParseSafetensors(b []byte) (*File, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("%w: %d bytes, shorter than the header length", ErrSafetensors, len(b))
	}
	n := binary.LittleEndian.Uint64(b)
	if n > maxHeader || n > uint64(len(b)-8) { //nolint:gosec // G115: len(b) >= 8.
		return nil, fmt.Errorf("%w: header length %d for a %d-byte file", ErrSafetensors, n, len(b))
	}
	data := b[8+n:]
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b[8:8+n], &raw); err != nil {
		return nil, fmt.Errorf("%w: header: %w", ErrSafetensors, err)
	}
	f := &File{Metadata: map[string]string{}, Tensors: make(map[string]Tensor, len(raw))}
	for name, msg := range raw {
		if name == metadataKey {
			if err := json.Unmarshal(msg, &f.Metadata); err != nil {
				return nil, fmt.Errorf("%w: metadata: %w", ErrSafetensors, err)
			}
			continue
		}
		t, err := parseEntry(name, msg, data)
		if err != nil {
			return nil, err
		}
		f.Tensors[name] = t
	}
	return f, nil
}

func parseEntry(name string, msg json.RawMessage, data []byte) (Tensor, error) {
	var h headerEntry
	if err := json.Unmarshal(msg, &h); err != nil {
		return Tensor{}, fmt.Errorf("%w: tensor %q: %w", ErrSafetensors, name, err)
	}
	size, ok := elemSize[h.DType]
	if !ok {
		return Tensor{}, fmt.Errorf("%w: tensor %q: unknown dtype %q", ErrSafetensors, name, h.DType)
	}
	begin, end := h.DataOffsets[0], h.DataOffsets[1]
	if begin < 0 || begin > end || end > int64(len(data)) {
		return Tensor{}, fmt.Errorf("%w: tensor %q: offsets %v outside %d data bytes", ErrSafetensors, name,
			h.DataOffsets, len(data))
	}
	want := int64(size)
	for _, d := range h.Shape {
		if d < 0 || (d > 0 && want > int64(len(data))/int64(d)) {
			return Tensor{}, fmt.Errorf("%w: tensor %q: shape %v", ErrSafetensors, name, h.Shape)
		}
		want *= int64(d)
	}
	if want != end-begin {
		return Tensor{}, fmt.Errorf("%w: tensor %q: shape %v %s needs %d bytes, offsets give %d", ErrSafetensors,
			name, h.Shape, h.DType, want, end-begin)
	}
	return Tensor{DType: h.DType, Shape: h.Shape, data: data[begin:end]}, nil
}

func encodeF64(name string, v []float64, meta map[string]string) ([]byte, error) {
	header := map[string]any{
		name: headerEntry{DType: dtypeF64, Shape: []int{len(v)}, DataOffsets: [2]int64{0, int64(8 * len(v))}},
	}
	if len(meta) > 0 {
		header[metadataKey] = meta
	}
	h, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("mf: encode header: %w", err)
	}
	if pad := (8 - len(h)%8) % 8; pad > 0 {
		h = append(h, strings.Repeat(" ", pad)...)
	}
	out := make([]byte, 8, 8+len(h)+8*len(v))
	binary.LittleEndian.PutUint64(out, uint64(len(h)))
	out = append(out, h...)
	for _, x := range v {
		out = binary.LittleEndian.AppendUint64(out, math.Float64bits(x))
	}
	return out, nil
}
