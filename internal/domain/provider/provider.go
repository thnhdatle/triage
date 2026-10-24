// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// Provider is the port to an upstream model API.
type Provider interface {
	Complete(ctx context.Context, req chat.Request) (chat.Response, error)
	Stream(ctx context.Context, req chat.Request) (Stream, error)
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
	Models(ctx context.Context) ([]string, error)
}

type Stream interface {
	Recv() (chat.Chunk, error)
	Close() error
}

type EmbedRequest struct {
	Model          string
	Input          []string
	Dimensions     int
	User           string
	EncodingFormat string
}

type EmbedResponse struct {
	Model   string
	Vectors [][]float32
	Usage   chat.Usage
}
