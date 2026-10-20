// SPDX-License-Identifier: Apache-2.0

// Package fakeprovider is a scriptable in-memory provider.Provider for tests.
// Each Complete or Stream call consumes the next scripted Outcome (a response,
// an error, or a stream script); when the queue is empty the call answers with
// a default response echoing the last user text. Every request is recorded as a
// deep copy.
//
//	p := fakeprovider.New(fakeprovider.WithOutcomes(fakeprovider.Fail(err)))
//	_, err := p.Complete(ctx, req) // scripted error, then echo responses
package fakeprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/testutil/fakeembedder"
)

// ErrStreamClosed is returned by Recv after the stream was closed.
var ErrStreamClosed = errors.New("fakeprovider: stream closed")

// StreamScript scripts a stream: Chunks are delivered in order, then Err, or
// io.EOF when Err is nil.
type StreamScript struct {
	Chunks []chat.Chunk
	Err    error
}

// Outcome is one scripted result. Err wins over Stream, which wins over
// Response. A stream script given to Complete is aggregated into a response.
type Outcome struct {
	Response *chat.Response
	Err      error
	Stream   *StreamScript
}

// Respond scripts a successful response.
func Respond(resp chat.Response) Outcome { return Outcome{Response: &resp} }

// Fail scripts a call error (returned before any stream starts).
func Fail(err error) Outcome { return Outcome{Err: err} }

// StreamChunks scripts a stream of chunks ending with err, or io.EOF when nil.
func StreamChunks(chunks []chat.Chunk, err error) Outcome {
	return Outcome{Stream: &StreamScript{Chunks: chunks, Err: err}}
}

// Option configures a Provider.
type Option func(*Provider)

// WithOutcomes queues outcomes consumed by successive Complete/Stream calls.
func WithOutcomes(o ...Outcome) Option {
	return func(p *Provider) { p.queue = append(p.queue, o...) }
}

// WithModels sets the names returned by Models.
func WithModels(names ...string) Option {
	return func(p *Provider) { p.models = append([]string(nil), names...) }
}

// WithModelsError makes Models fail with err.
func WithModelsError(err error) Option { return func(p *Provider) { p.modelsErr = err } }

// WithEmbedError makes Embed fail with err.
func WithEmbedError(err error) Option { return func(p *Provider) { p.embedErr = err } }

// Provider implements provider.Provider. It is safe for concurrent use.
type Provider struct {
	mu        sync.Mutex
	queue     []Outcome
	requests  []chat.Request
	embeds    []provider.EmbedRequest
	models    []string
	modelsErr error
	embedErr  error
}

// New returns a Provider with the given options.
func New(opts ...Option) *Provider {
	p := &Provider{}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Enqueue appends outcomes to the script queue.
func (p *Provider) Enqueue(o ...Outcome) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = append(p.queue, o...)
}

// record stores a deep copy of req and pops the next outcome, if any.
func (p *Provider) record(req chat.Request) (Outcome, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req.Clone())
	if len(p.queue) == 0 {
		return Outcome{}, false
	}
	o := p.queue[0]
	p.queue = p.queue[1:]
	return o, true
}

// Complete implements provider.Provider.
func (p *Provider) Complete(ctx context.Context, req chat.Request) (chat.Response, error) {
	o, scripted := p.record(req)
	if err := ctx.Err(); err != nil {
		return chat.Response{}, fmt.Errorf("fakeprovider: %w", err)
	}
	switch {
	case !scripted:
		return echoResponse(req), nil
	case o.Err != nil:
		return chat.Response{}, o.Err
	case o.Stream != nil:
		return aggregate(req, *o.Stream)
	case o.Response != nil:
		return *o.Response, nil
	}
	return echoResponse(req), nil
}

// Stream implements provider.Provider. A scripted Response is delivered as a
// content chunk plus a finish chunk (with usage when req.IncludeUsage).
func (p *Provider) Stream(ctx context.Context, req chat.Request) (provider.Stream, error) {
	o, scripted := p.record(req)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fakeprovider: %w", err)
	}
	if scripted && o.Err != nil {
		return nil, o.Err
	}
	if scripted && o.Stream != nil {
		return newStream(ctx, *o.Stream), nil
	}
	resp := echoResponse(req)
	if scripted && o.Response != nil {
		resp = *o.Response
	}
	return newStream(ctx, StreamScript{Chunks: responseChunks(req, resp)}), nil
}

// Embed implements provider.Provider with deterministic unit vectors.
func (p *Provider) Embed(ctx context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error) {
	p.mu.Lock()
	p.embeds = append(p.embeds, provider.EmbedRequest{
		Model: req.Model, Input: append([]string(nil), req.Input...), Dimensions: req.Dimensions,
		User: req.User, EncodingFormat: req.EncodingFormat,
	})
	err := p.embedErr
	p.mu.Unlock()

	if err != nil {
		return provider.EmbedResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return provider.EmbedResponse{}, fmt.Errorf("fakeprovider: %w", err)
	}
	dims := req.Dimensions
	if dims < 1 {
		dims = fakeembedder.DefaultDimensions
	}
	vecs := make([][]float32, len(req.Input))
	tokens := 0
	for i, in := range req.Input {
		vecs[i] = fakeembedder.Vector(in, dims)
		tokens += chat.EstimateTokens(in)
	}
	return provider.EmbedResponse{
		Model:   req.Model,
		Vectors: vecs,
		Usage:   chat.Usage{PromptTokens: tokens},
	}, nil
}

// Models implements provider.Provider.
func (p *Provider) Models(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	names := append([]string(nil), p.models...)
	err := p.modelsErr
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fakeprovider: %w", err)
	}
	return names, nil
}

// Requests returns deep copies of every Complete and Stream request received,
// in arrival order.
func (p *Provider) Requests() []chat.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]chat.Request, len(p.requests))
	for i, r := range p.requests {
		out[i] = r.Clone()
	}
	return out
}

// EmbedRequests returns every embeddings request received, in arrival order.
func (p *Provider) EmbedRequests() []provider.EmbedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.EmbedRequest(nil), p.embeds...)
}

func echoResponse(req chat.Request) chat.Response {
	text := req.LastUserText()
	return chat.Response{
		ID:      "fake-response",
		Model:   req.Model,
		Created: 1,
		Choices: []chat.Choice{{
			Message:      chat.TextMessage(chat.RoleAssistant, text),
			FinishReason: chat.FinishStop,
		}},
		Usage: chat.Usage{
			PromptTokens:     req.PromptTokens(nil),
			CompletionTokens: chat.EstimateTokens(text),
		},
	}
}

func responseChunks(req chat.Request, resp chat.Response) []chat.Chunk {
	var text string
	finish := chat.FinishStop
	if len(resp.Choices) > 0 {
		text = resp.Choices[0].Message.Text()
		if resp.Choices[0].FinishReason != "" {
			finish = resp.Choices[0].FinishReason
		}
	}
	chunks := []chat.Chunk{
		{
			ID: resp.ID, Model: resp.Model, Created: resp.Created,
			Choices: []chat.ChunkChoice{{Delta: chat.Delta{Role: chat.RoleAssistant, Content: text}}},
		},
		{
			ID: resp.ID, Model: resp.Model, Created: resp.Created,
			Choices: []chat.ChunkChoice{{FinishReason: finish}},
		},
	}
	if req.IncludeUsage {
		u := resp.Usage
		chunks = append(chunks, chat.Chunk{ID: resp.ID, Model: resp.Model, Created: resp.Created, Usage: &u})
	}
	return chunks
}

func aggregate(req chat.Request, s StreamScript) (chat.Response, error) {
	if s.Err != nil {
		return chat.Response{}, s.Err
	}
	resp := echoResponse(req)
	var text []byte
	for _, c := range s.Chunks {
		if c.ID != "" {
			resp.ID = c.ID
		}
		for _, ch := range c.Choices {
			text = append(text, ch.Delta.Content...)
			if ch.FinishReason != "" {
				resp.Choices[0].FinishReason = ch.FinishReason
			}
		}
		if c.Usage != nil {
			resp.Usage = *c.Usage
		}
	}
	resp.Choices[0].Message = chat.TextMessage(chat.RoleAssistant, string(text))
	return resp, nil
}

// stream is the fake provider.Stream.
type stream struct {
	ctx    context.Context //nolint:containedctx // the stream honours the context of the call that created it
	script StreamScript

	mu     sync.Mutex
	pos    int
	closed bool
}

func newStream(ctx context.Context, s StreamScript) *stream {
	return &stream{ctx: ctx, script: s}
}

// Recv implements provider.Stream.
func (s *stream) Recv() (chat.Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return chat.Chunk{}, ErrStreamClosed
	}
	if err := s.ctx.Err(); err != nil {
		return chat.Chunk{}, fmt.Errorf("fakeprovider: %w", err)
	}
	if s.pos < len(s.script.Chunks) {
		c := s.script.Chunks[s.pos]
		s.pos++
		return c, nil
	}
	if s.script.Err != nil {
		return chat.Chunk{}, s.script.Err
	}
	return chat.Chunk{}, io.EOF
}

// Close implements provider.Stream; it is idempotent.
func (s *stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
