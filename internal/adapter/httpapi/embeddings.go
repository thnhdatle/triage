// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

type wireEmbedRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	Dimensions     int             `json:"dimensions"`
	User           string          `json:"user"`
	EncodingFormat string          `json:"encoding_format"`
}

type wireEmbedResponse struct {
	Object string          `json:"object"`
	Data   []wireEmbedding `json:"data"`
	Model  string          `json:"model"`
	Usage  wireEmbedUsage  `json:"usage"`
}

type wireEmbedding struct {
	Object    string `json:"object"`
	Index     int    `json:"index"`
	Embedding any    `json:"embedding"`
}

type wireEmbedUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func decodeEmbed(body []byte) (provider.EmbedRequest, *apiError) {
	var w wireEmbedRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return provider.EmbedRequest{}, invalid("", "invalid JSON body: "+err.Error())
	}
	if w.Model == "" {
		return provider.EmbedRequest{}, invalid(paramModel, "model is required")
	}
	input, aerr := decodeInput(w.Input)
	if aerr != nil {
		return provider.EmbedRequest{}, aerr
	}
	if w.Dimensions < 0 {
		return provider.EmbedRequest{}, invalid("dimensions", "dimensions must not be negative")
	}
	switch w.EncodingFormat {
	case "", "float", "base64":
	default:
		return provider.EmbedRequest{}, invalid("encoding_format", "encoding_format must be float or base64")
	}
	return provider.EmbedRequest{
		Model: w.Model, Input: input, Dimensions: w.Dimensions, User: w.User, EncodingFormat: w.EncodingFormat,
	}, nil
}

func decodeInput(raw json.RawMessage) ([]string, *apiError) {
	const msg = "input must be a string or a non-empty array of strings"
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil || len(many) == 0 {
		return nil, invalid("input", msg)
	}
	return many, nil
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, aerr := readBody(w, r, s.current().MaxBodyBytes)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	req, aerr := decodeEmbed(body)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	out, err := s.router.Embeddings(r.Context(), route.EmbeddingsRequest{
		ID: s.newID(), Headers: requestHeaders(r.Header), Request: req,
	})
	s.setOutcomeHeaders(w, out)
	if err != nil {
		writeAPIError(w, fromError(err))
		return
	}
	writeJSON(w, http.StatusOK, encodeEmbeddings(out.Embeddings, req.EncodingFormat == "base64"))
}

func encodeEmbeddings(resp provider.EmbedResponse, b64 bool) wireEmbedResponse {
	out := wireEmbedResponse{
		Object: "list", Model: resp.Model, Data: make([]wireEmbedding, 0, len(resp.Vectors)),
		Usage: wireEmbedUsage{PromptTokens: resp.Usage.PromptTokens, TotalTokens: resp.Usage.PromptTokens},
	}
	for i, v := range resp.Vectors {
		e := wireEmbedding{Object: "embedding", Index: i, Embedding: v}
		if b64 {
			e.Embedding = encodeFloat32s(v)
		}
		out.Data = append(out.Data, e)
	}
	return out
}

// encodeFloat32s is OpenAI's base64 format: little-endian float32 values.
func encodeFloat32s(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(b)
}
