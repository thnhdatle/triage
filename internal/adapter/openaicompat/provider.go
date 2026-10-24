// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

var (
	_ provider.Provider   = (*Client)(nil)
	_ difficulty.Embedder = Embedder{}
	_ provider.Stream     = (*stream)(nil)
)

// Complete sends a chat completion request.
func (c *Client) Complete(ctx context.Context, req chat.Request) (chat.Response, error) {
	body, err := encodeRequest(req, false)
	if err != nil {
		return chat.Response{}, err
	}
	data, status, err := c.call(ctx, http.MethodPost, "/chat/completions", req.Model, body)
	if err != nil {
		return chat.Response{}, err
	}
	return decodeResponse(data, status)
}

// Stream sends a streaming chat completion request.
func (c *Client) Stream(ctx context.Context, req chat.Request) (provider.Stream, error) {
	body, err := encodeRequest(req, true)
	if err != nil {
		return nil, err
	}
	//nolint:bodyclose // the body is owned and closed by the returned stream
	resp, err := c.send(ctx, http.MethodPost, "/chat/completions", req.Model, body)
	if err != nil {
		return nil, err
	}
	return newStream(ctx, resp, c.maxBytes), nil
}

type wireEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	User           string   `json:"user,omitempty"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
}

type wireEmbedResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Index     int             `json:"index"`
		Embedding json.RawMessage `json:"embedding"`
	} `json:"data"`
	Usage *wireUsage `json:"usage"`
}

// Embed sends an embeddings request.
func (c *Client) Embed(ctx context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error) {
	body, err := json.Marshal(wireEmbedRequest{
		Model: req.Model, Input: req.Input, Dimensions: req.Dimensions, User: req.User,
		EncodingFormat: req.EncodingFormat,
	})
	if err != nil {
		return provider.EmbedResponse{}, fmt.Errorf("openaicompat: encode embeddings request: %w", err)
	}
	data, status, err := c.call(ctx, http.MethodPost, "/embeddings", req.Model, body)
	if err != nil {
		return provider.EmbedResponse{}, err
	}
	var w wireEmbedResponse
	if err := json.Unmarshal(data, &w); err != nil {
		return provider.EmbedResponse{}, malformed(status, "invalid JSON")
	}
	if len(w.Data) != len(req.Input) {
		return provider.EmbedResponse{}, malformed(status, "embedding count does not match input count")
	}
	vecs := make([][]float32, len(req.Input))
	for _, d := range w.Data {
		if d.Index < 0 || d.Index >= len(vecs) || vecs[d.Index] != nil {
			return provider.EmbedResponse{}, malformed(status, "invalid embedding index")
		}
		v, ok := decodeVector(d.Embedding)
		if !ok {
			return provider.EmbedResponse{}, malformed(status, "invalid embedding")
		}
		vecs[d.Index] = v
	}
	return provider.EmbedResponse{Model: w.Model, Vectors: vecs, Usage: w.Usage.usage()}, nil
}

func decodeVector(raw json.RawMessage) ([]float32, bool) {
	var v []float32
	if json.Unmarshal(raw, &v) == nil {
		return v, v != nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%4 != 0 || len(b) == 0 {
		return nil, false
	}
	v = make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, true
}

// Models lists the model ids the backend serves.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	data, status, err := c.call(ctx, http.MethodGet, "/models", "", nil)
	if err != nil {
		return nil, err
	}
	var w struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &w); err != nil || w.Data == nil {
		return nil, malformed(status, "invalid model list")
	}
	names := make([]string, 0, len(w.Data))
	for _, m := range w.Data {
		names = append(names, m.ID)
	}
	return names, nil
}

type Embedder struct {
	c *Client
}

func (c *Client) Embedder() Embedder { return Embedder{c: c} }

func (e Embedder) Embed(ctx context.Context, model string, dimensions int, texts []string) ([][]float32, error) {
	resp, err := e.c.Embed(ctx, provider.EmbedRequest{Model: model, Input: texts, Dimensions: dimensions})
	if err != nil {
		return nil, err
	}
	return resp.Vectors, nil
}
