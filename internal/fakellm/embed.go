// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
)

const (
	defaultDimensions = 1024
	maxDimensions     = 16384
	maxInputs         = 4096
)

type embeddingRequest struct {
	Model      string          `json:"model"`
	Input      json.RawMessage `json:"input"`
	Dimensions *int            `json:"dimensions"`
}

func parseInputs(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("input is required")
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, errors.New("input must be a string or an array of strings")
	}
	if len(many) == 0 || len(many) > maxInputs {
		return nil, fmt.Errorf("input must contain between 1 and %d strings", maxInputs)
	}
	return many, nil
}

func embed(model, text string, dim int) []float32 {
	seed := []byte(model + "\x00" + text)
	root := sha256.Sum256(seed)
	vals := make([]float64, 0, dim+8)
	var ctr [4]byte
	for i := uint32(0); len(vals) < dim; i++ {
		binary.BigEndian.PutUint32(ctr[:], i)
		blk := sha256.Sum256(append(root[:], ctr[:]...))
		for j := 0; j+4 <= len(blk) && len(vals) < dim; j += 4 {
			u := int32(binary.BigEndian.Uint32(blk[j : j+4])) //nolint:gosec // G115: reinterpreting hash bits
			vals = append(vals, float64(u)/(1<<31))
		}
	}
	var sum float64
	for _, v := range vals {
		sum += v * v
	}
	norm := math.Sqrt(sum)
	out := make([]float32, dim)
	for i, v := range vals {
		if norm > 0 {
			v /= norm
		}
		out[i] = float32(v)
	}
	return out
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxBodyBytes)
	if !ok {
		return
	}
	var req embeddingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.record(r, body, "", KindEmbeddings, 0)
		badRequest(w, "invalid JSON body: "+err.Error())
		return
	}
	model := req.Model
	if model == "" {
		model = DefaultModel
	}
	inputs, err := parseInputs(req.Input)
	if err != nil {
		s.record(r, body, model, KindEmbeddings, 0)
		badRequest(w, err.Error())
		return
	}
	s.record(r, body, model, KindEmbeddings, len(inputs))
	dim := defaultDimensions
	if req.Dimensions != nil {
		dim = *req.Dimensions
	}
	if dim < 1 || dim > maxDimensions {
		badRequest(w, fmt.Sprintf("dimensions must be between 1 and %d", maxDimensions))
		return
	}
	if !s.touch(model) {
		badRequest(w, "too many distinct models")
		return
	}

	type item struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	data := make([]item, 0, len(inputs))
	tokens := 0
	for i, text := range inputs {
		data = append(data, item{Object: "embedding", Index: i, Embedding: embed(model, text, dim)})
		tokens += tokenCount(text)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		keyObject: objectList,
		"data":    data,
		keyModel:  model,
		"usage":   map[string]int{"prompt_tokens": tokens, "total_tokens": tokens},
	})
}
