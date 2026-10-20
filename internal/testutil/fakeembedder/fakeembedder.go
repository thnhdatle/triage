// SPDX-License-Identifier: Apache-2.0

// Package fakeembedder is a deterministic in-memory difficulty.Embedder for
// tests. Vectors are unit vectors derived from SHA-256 of the text, so equal
// texts always map to equal vectors and different texts almost surely differ.
//
//	e := fakeembedder.New(8)
//	vecs, _ := e.Embed(ctx, "embed-model", 0, []string{"hello"})
package fakeembedder

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"sync"
)

// DefaultDimensions is the vector size used when none is requested.
const DefaultDimensions = 8

// Embedder implements difficulty.Embedder deterministically and records calls.
type Embedder struct {
	dims int

	mu    sync.Mutex
	calls int
	texts []string
	err   error
}

// New returns an Embedder producing vectors of dims floats when the caller
// requests no dimensions. A dims below 1 selects DefaultDimensions.
func New(dims int) *Embedder {
	if dims < 1 {
		dims = DefaultDimensions
	}
	return &Embedder{dims: dims}
}

// SetError makes every following Embed call fail with err; nil clears it.
func (e *Embedder) SetError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = err
}

// Embed returns one deterministic unit vector per text. A positive dimensions
// overrides the default size. The call is counted and its texts recorded even
// when it fails.
func (e *Embedder) Embed(ctx context.Context, _ string, dimensions int, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	e.texts = append(e.texts, texts...)
	err := e.err
	e.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fakeembedder: %w", err)
	}
	n := e.dims
	if dimensions > 0 {
		n = dimensions
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = Vector(t, n)
	}
	return out, nil
}

// Calls returns the number of Embed calls so far.
func (e *Embedder) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// Texts returns every text embedded so far, in call order.
func (e *Embedder) Texts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.texts...)
}

// Vector returns the deterministic unit vector of n floats for text.
func Vector(text string, n int) []float32 {
	if n < 1 {
		return []float32{}
	}
	raw := make([]float64, n)
	var norm float64
	for i := range raw {
		sum := sha256.Sum256([]byte(strconv.Itoa(i/8) + "|" + text))
		bits := binary.BigEndian.Uint32(sum[(i%8)*4:])
		// Map to [-1, 1].
		raw[i] = float64(bits)/float64(math.MaxUint32)*2 - 1
		norm += raw[i] * raw[i]
	}
	out := make([]float32, n)
	if norm == 0 {
		out[0] = 1
		return out
	}
	norm = math.Sqrt(norm)
	for i, v := range raw {
		out[i] = float32(v / norm)
	}
	return out
}
