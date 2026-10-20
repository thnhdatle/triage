// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package piionnx

import (
	"encoding/binary"
	"math"
)

// This file hand-encodes a tiny ONNX token-classification model (onnx.proto3
// field numbers), the same approach as internal/platform/onnxrt's tests, so
// no weights are downloaded and no Python or protobuf dependency is needed.
// The output is deterministic.

// ONNX TensorProto.DataType values.
const (
	onnxFloat = 1
	onnxInt64 = 7
)

type pb []byte

func (b pb) tag(field, wire int) pb { return binary.AppendUvarint(b, uint64(field<<3|wire)) }

func (b pb) varint(field int, v int64) pb {
	return binary.AppendUvarint(b.tag(field, 0), uint64(v)) //nolint:gosec // G115: protobuf int64 is two's complement
}

func (b pb) bytes(field int, p []byte) pb {
	b = binary.AppendUvarint(b.tag(field, 2), uint64(len(p)))
	return append(b, p...)
}

func (b pb) str(field int, s string) pb { return b.bytes(field, []byte(s)) }

// valueInfo encodes a ValueInfoProto of a tensor; a dim is an int or a
// symbolic name.
func valueInfo(name string, elem int, dims ...any) pb {
	var shape pb
	for _, d := range dims {
		var dp pb
		switch v := d.(type) {
		case int:
			dp = dp.varint(1, int64(v))
		case string:
			dp = dp.str(2, v)
		}
		shape = shape.bytes(1, dp)
	}
	tensor := pb{}.varint(1, int64(elem)).bytes(2, shape)
	return pb{}.str(1, name).bytes(2, pb{}.bytes(1, tensor))
}

func attrInt(name string, v int64) pb { return pb{}.str(1, name).varint(3, v).varint(20, 2) }

func node(op string, in, out []string, attrs ...pb) pb {
	var n pb
	for _, s := range in {
		n = n.str(1, s)
	}
	for _, s := range out {
		n = n.str(2, s)
	}
	n = n.str(3, op+"_"+out[0]).str(4, op)
	for _, a := range attrs {
		n = n.bytes(5, a)
	}
	return n
}

func floatInit(name string, dims []int64, data []float32) pb {
	var t pb
	for _, d := range dims {
		t = t.varint(1, d)
	}
	raw := make([]byte, 0, 4*len(data))
	for _, f := range data {
		raw = binary.LittleEndian.AppendUint32(raw, math.Float32bits(f))
	}
	return t.varint(2, onnxFloat).str(8, name).bytes(9, raw)
}

func int64Init(name string, dims []int64, data []int64) pb {
	var t pb
	for _, d := range dims {
		t = t.varint(1, d)
	}
	raw := make([]byte, 0, 8*len(data))
	for _, v := range data {
		raw = binary.LittleEndian.AppendUint64(raw, uint64(v)) //nolint:gosec // G115: two's complement
	}
	return t.varint(2, onnxInt64).str(8, name).bytes(9, raw)
}

// tokenModel is a token-classification model whose logits depend only on the
// token id: logits[b,s,:] = E[input_ids[b,s],:]. The attention mask and the
// token type ids are consumed (multiplied by zero), so the graph has the
// three inputs of a Hugging Face BERT export and one output "logits"
// [batch, seq, labels]. inputs overrides the input names when non-nil.
func tokenModel(vocab, labels int, rows map[int][]float32, inputs []string) []byte {
	if inputs == nil {
		inputs = []string{InputIDs, InputAttentionMask, InputTokenTypeIDs}
	}
	emb := make([]float32, vocab*labels)
	for id, row := range rows {
		copy(emb[id*labels:(id+1)*labels], row)
	}
	nodes := []pb{
		node("Gather", []string{"E", inputs[0]}, []string{"g"}, attrInt("axis", 0)),
		node("Cast", []string{inputs[1]}, []string{"mf"}, attrInt("to", onnxFloat)),
		node("Cast", []string{inputs[2]}, []string{"tf"}, attrInt("to", onnxFloat)),
		node("Add", []string{"mf", "tf"}, []string{"mt"}),
		node("Mul", []string{"mt", "zero"}, []string{"z"}),
		node("Unsqueeze", []string{"z", "axes"}, []string{"z3"}),
		node("Add", []string{"g", "z3"}, []string{OutputLogits}),
	}
	var g pb
	for _, n := range nodes {
		g = g.bytes(1, n)
	}
	g = g.str(2, "ner-test")
	g = g.bytes(5, floatInit("E", []int64{int64(vocab), int64(labels)}, emb))
	g = g.bytes(5, floatInit("zero", []int64{1}, []float32{0}))
	g = g.bytes(5, int64Init("axes", []int64{1}, []int64{-1}))
	for _, name := range inputs {
		g = g.bytes(11, valueInfo(name, onnxInt64, "batch", "seq"))
	}
	g = g.bytes(12, valueInfo(OutputLogits, onnxFloat, "batch", "seq", labels))
	opset := pb{}.str(1, "").varint(2, 13)
	return pb{}.varint(1, 8).str(2, "piionnx-test").bytes(7, g).bytes(8, opset)
}
