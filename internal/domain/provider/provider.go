// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// Provider is one upstream backend. The request's Model field carries the
// target's upstream model name.
type Provider interface {
	Complete(ctx context.Context, req chat.Request) (chat.Response, error)
	// Stream starts a streaming completion. The returned Stream must be closed.
	Stream(ctx context.Context, req chat.Request) (Stream, error)
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
	// Models lists the upstream's model names (used by the health probe).
	Models(ctx context.Context) ([]string, error)
}

// Stream yields chunks of a streaming completion.
type Stream interface {
	// Recv returns the next chunk, io.EOF after the terminal event, or a
	// provider error (e.g. ErrUpstreamUnavailable on a cut connection).
	Recv() (chat.Chunk, error)
	Close() error
}

// EmbedRequest is a provider-neutral embeddings request.
type EmbedRequest struct {
	Model string
	Input []string
	// Dimensions is the requested vector size; 0 means the model default.
	Dimensions int
	User       string
	// EncodingFormat is passed through ("float" or "base64"); empty means float.
	EncodingFormat string
}

// EmbedResponse holds one vector per input, in input order.
type EmbedResponse struct {
	Model   string
	Vectors [][]float32
	Usage   chat.Usage
}
