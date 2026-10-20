// SPDX-License-Identifier: Apache-2.0

package mf_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// TestMain checks for leaked goroutines (embedder fakes are called
// concurrently in some tests).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	mfDir  = "../../../../testdata/models/mf-tiny"
	embDir = "../../../../testdata/models/embeddings"
)

func userReq(text string) chat.Request {
	return chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: text}}},
	}}
}

// st is one tensor for buildST.
type st struct {
	dtype string
	shape []int
	f32   []float32
	raw   []byte // used instead of f32 when set
}

// buildST serialises tensors in name order (F32 data or raw bytes).
func buildST(t *testing.T, tensors map[string]st, meta map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(tensors))
	for n := range tensors {
		names = append(names, n)
	}
	sort.Strings(names)
	header := map[string]any{}
	if meta != nil {
		header["__metadata__"] = meta
	}
	var data []byte
	for _, n := range names {
		ts := tensors[n]
		b := ts.raw
		if b == nil {
			for _, x := range ts.f32 {
				b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
			}
		}
		dtype := ts.dtype
		if dtype == "" {
			dtype = "F32"
		}
		header[n] = map[string]any{"dtype": dtype, "shape": ts.shape, "data_offsets": []int{len(data), len(data) + len(b)}}
		data = append(data, b...)
	}
	h, err := json.Marshal(header)
	require.NoError(t, err)
	out := binary.LittleEndian.AppendUint64(nil, uint64(len(h)))
	return append(append(out, h...), data...)
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

type reference struct {
	StrongRow int       `json:"strong_row"`
	WeakRow   int       `json:"weak_row"`
	U         []float64 `json:"u"`
	Scores    []struct {
		ID       string  `json:"id"`
		PMFModel float64 `json:"p_mfmodel"`
		PStrong  float64 `json:"p_strong"`
	} `json:"scores"`
}

func loadReference(t *testing.T) reference {
	t.Helper()
	var r reference
	require.NoError(t, json.Unmarshal(readFile(t, filepath.Join(mfDir, "reference.json")), &r))
	return r
}

// embeddings returns the fixture vectors keyed by meta.json ids.
func embeddings(t *testing.T) (ids []string, vecs map[string][]float32) {
	t.Helper()
	var meta struct {
		Dimensions int      `json:"dimensions"`
		Count      int      `json:"count"`
		IDs        []string `json:"ids"`
	}
	require.NoError(t, json.Unmarshal(readFile(t, filepath.Join(embDir, "meta.json")), &meta))
	f, err := mf.ParseSafetensors(readFile(t, filepath.Join(embDir, "embeddings.safetensors")))
	require.NoError(t, err)
	tn := f.Tensors["embeddings"]
	require.Equal(t, []int{meta.Count, meta.Dimensions}, tn.Shape)
	all, err := tn.Float64s()
	require.NoError(t, err)
	vecs = make(map[string][]float32, meta.Count)
	for i, id := range meta.IDs {
		v := make([]float32, meta.Dimensions)
		for j := range v {
			v[j] = float32(all[i*meta.Dimensions+j])
		}
		vecs[id] = v
	}
	return meta.IDs, vecs
}

// tableProvider is an EmbedProvider answering from a table, with scripted
// usage and error; it records the requests.
type tableProvider struct {
	mu    sync.Mutex
	vecs  map[string][]float32
	usage int
	err   error
	extra int // extra vectors appended to every answer
	reqs  []provider.EmbedRequest
}

func (p *tableProvider) Embed(_ context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return provider.EmbedResponse{}, p.err
	}
	out := make([][]float32, 0, len(req.Input)+p.extra)
	for _, text := range req.Input {
		v, ok := p.vecs[text]
		if !ok {
			return provider.EmbedResponse{}, fmt.Errorf("no vector for %q", text)
		}
		out = append(out, v)
	}
	for range p.extra {
		out = append(out, nil)
	}
	return provider.EmbedResponse{Model: req.Model, Vectors: out, Usage: chat.Usage{PromptTokens: p.usage}}, nil
}

func (p *tableProvider) requests() []provider.EmbedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.EmbedRequest(nil), p.reqs...)
}
