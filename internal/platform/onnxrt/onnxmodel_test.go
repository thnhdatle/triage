// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package onnxrt

import (
	"encoding/binary"
	"math"
	"strconv"
)

const (
	onnxFloat = 1
	onnxInt32 = 6
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

type dim any

func valueInfo(name string, elem int, dims ...dim) pb {
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
	typ := pb{}.bytes(1, tensor)
	return pb{}.str(1, name).bytes(2, typ)
}

func attrInt(name string, v int64) pb {
	return pb{}.str(1, name).varint(3, v).varint(20, 2)
}

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

type graphSpec struct {
	nodes, inits, inputs, outputs []pb
}

func model(g graphSpec) []byte {
	var gp pb
	for _, n := range g.nodes {
		gp = gp.bytes(1, n)
	}
	gp = gp.str(2, "test")
	for _, i := range g.inits {
		gp = gp.bytes(5, i)
	}
	for _, i := range g.inputs {
		gp = gp.bytes(11, i)
	}
	for _, o := range g.outputs {
		gp = gp.bytes(12, o)
	}
	opset := pb{}.str(1, "").varint(2, 13)
	return pb{}.varint(1, 8).str(2, "onnxrt-test").bytes(7, gp).bytes(8, opset)
}

var (
	addW = []float32{
		1, 0, 0,
		0, 1, 0,
		0, 0, 1,
		1, 1, 1,
	}
	addB = []float32{10, 0, 0}
)

func addModel() []byte {
	return model(graphSpec{
		nodes: []pb{
			node("Cast", []string{"ids"}, []string{"idsf"}, attrInt("to", onnxFloat)),
			node("Add", []string{"idsf", "x"}, []string{"h"}),
			node("MatMul", []string{"h", "W"}, []string{"mm"}),
			node("Add", []string{"mm", "B"}, []string{"logits"}),
			node("ArgMax", []string{"logits"}, []string{"top"}, attrInt("axis", 1), attrInt("keepdims", 0)),
		},
		inits:   []pb{floatInit("W", []int64{4, 3}, addW), floatInit("B", []int64{3}, addB)},
		inputs:  []pb{valueInfo("ids", onnxInt64, "batch", 4), valueInfo("x", onnxFloat, "batch", 4)},
		outputs: []pb{valueInfo("logits", onnxFloat, "batch", 3), valueInfo("top", onnxInt64, "batch")},
	})
}

func addReference(ids []int64, x []float32) (logits []float32, top []int64) {
	for r := range len(ids) / 4 {
		var h [4]float32
		for c := range 4 {
			h[c] = float32(ids[r*4+c]) + x[r*4+c]
		}
		best := 0
		row := make([]float32, 3)
		for j := range 3 {
			for k := range 4 {
				row[j] += h[k] * addW[k*3+j]
			}
			row[j] += addB[j]
			if row[j] > row[best] {
				best = j
			}
		}
		logits = append(logits, row...)
		top = append(top, int64(best))
	}
	return logits, top
}

func castModel(to int) []byte {
	return model(graphSpec{
		nodes:   []pb{node("Cast", []string{"x"}, []string{"y"}, attrInt("to", int64(to)))},
		inputs:  []pb{valueInfo("x", onnxFloat, 1)},
		outputs: []pb{valueInfo("y", to, 1)},
	})
}

func slowModel(n, k int) []byte {
	nodes := make([]pb, 0, k)
	prev := "x"
	for i := range k {
		out := "y" + strconv.Itoa(i)
		if i == k-1 {
			out = "y"
		}
		nodes = append(nodes, node("MatMul", []string{prev, "x"}, []string{out}))
		prev = out
	}
	return model(graphSpec{
		nodes:   nodes,
		inputs:  []pb{valueInfo("x", onnxFloat, n, n)},
		outputs: []pb{valueInfo("y", onnxFloat, n, n)},
	})
}
