// SPDX-License-Identifier: Apache-2.0

package bert_test

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
)

const modelsDir = "../../../../testdata/models/bert-tiny"

type pb []byte

func (b pb) tag(num, wt int) pb { return binary.AppendUvarint(b, uint64(num<<3|wt)) }
func (b pb) varint(v uint64) pb { return binary.AppendUvarint(b, v) }
func (b pb) bytes(num int, p []byte) pb {
	return append(b.tag(num, 2).varint(uint64(len(p))), p...)
}

func entry(k, v string) []byte { return pb{}.bytes(1, []byte(k)).bytes(2, []byte(v)) }

func TestReadMetadataFixtures(t *testing.T) {
	t.Parallel()
	fp32, err := bert.ReadMetadataFile(filepath.Join(modelsDir, bert.FP32FileName))
	require.NoError(t, err)
	assert.Equal(t, "model.safetensors", fp32[bert.MetaWeightsFile])
	assert.Equal(t, "8022060", fp32[bert.MetaWeightsSize])
	assert.Empty(t, fp32[bert.MetaVariant])
	int8Model, err := bert.ReadMetadataFile(filepath.Join(modelsDir, bert.INT8FileName))
	require.NoError(t, err)
	assert.Equal(t, "int8", int8Model[bert.MetaVariant])
	assert.Equal(t, "blockwise", int8Model["triage.int8.scheme"])
	assert.Equal(t, fp32[bert.MetaWeightsSHA256], int8Model[bert.MetaWeightsSHA256])

	_, err = bert.ReadMetadataFile(filepath.Join(t.TempDir(), "missing.onnx"))
	require.Error(t, err)
}

func TestReadMetadataWire(t *testing.T) {
	t.Parallel()
	ok := pb{}.tag(1, 0).varint(8)
	ok = append(ok.tag(3, 1), make([]byte, 8)...)
	ok = append(ok.tag(4, 5), make([]byte, 4)...)
	ok = ok.bytes(7, []byte("graph bytes"))
	ok = ok.bytes(14, entry("a", "1"))
	ok = ok.bytes(14, append(entry("b", "2"), pb{}.bytes(9, []byte("ignored"))...))
	ok = ok.bytes(14, entry("a", "3"))
	ok = ok.tag(14, 0).varint(5)

	tests := []struct {
		name    string
		in      []byte
		want    map[string]string
		wantErr bool
	}{
		{name: "empty", in: nil, want: map[string]string{}},
		{name: "fields skipped, later key wins", in: ok, want: map[string]string{"a": "3", "b": "2"}},
		{name: "field number zero", in: pb{}.tag(0, 0).varint(1), wantErr: true},
		{name: "truncated tag", in: []byte{0x80}, wantErr: true},
		{name: "truncated varint", in: pb{}.tag(1, 0).varint(1 << 20)[:2], wantErr: true},
		{name: "truncated fixed64", in: append(pb{}.tag(3, 1), 1, 2), wantErr: true},
		{name: "truncated bytes", in: pb{}.bytes(7, []byte("abcdef"))[:4], wantErr: true},
		{name: "bad length", in: append(pb{}.tag(7, 2), 0x80), wantErr: true},
		{name: "group wire type", in: pb{}.tag(5, 3), wantErr: true},
		{name: "entry too long", in: pb{}.tag(14, 2).varint(1 << 21), wantErr: true},
		{name: "truncated entry", in: pb{}.tag(14, 2).varint(10).bytes(1, []byte("k")), wantErr: true},
		{name: "entry bad tag", in: pb{}.bytes(14, []byte{0x80}), wantErr: true},
		{name: "entry varint field", in: pb{}.bytes(14, pb{}.tag(1, 0).varint(1)), wantErr: true},
		{name: "entry truncated string", in: pb{}.bytes(14, pb{}.tag(1, 2).varint(9)), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := bert.ReadMetadata(bytes.NewReader(tc.in))
			if tc.wantErr {
				require.ErrorIs(t, err, bert.ErrInvalidModel)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func FuzzReadMetadata(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte(pb{}.bytes(14, entry("k", "v"))))
	f.Add([]byte(pb{}.tag(1, 0).varint(3).bytes(7, []byte("g"))))
	f.Fuzz(func(t *testing.T, in []byte) {
		meta, err := bert.ReadMetadata(bytes.NewReader(in))
		if err != nil {
			require.ErrorIs(t, err, bert.ErrInvalidModel)
			return
		}
		require.NotNil(t, meta)
	})
}
