// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

//nolint:gosec // G101: attribute names, not credentials
const (
	attrResponseModel = "gen_ai.response.model"
	attrInputTokens   = "gen_ai.usage.input_tokens"
	attrOutputTokens  = "gen_ai.usage.output_tokens"
	attrOperation     = "gen_ai.operation.name"
	attrRequestModel  = "gen_ai.request.model"
)

type tracedProvider struct {
	next    provider.Provider
	name    string
	tel     *telemetry.Telemetry
	clk     clock.Clock
	targets map[string]string
}

var _ provider.Provider = (*tracedProvider)(nil)

func (p *tracedProvider) target(modelName string) string {
	if t, ok := p.targets[modelName]; ok {
		return t
	}
	return modelName
}

func (p *tracedProvider) begin(ctx context.Context, op, modelName string) (context.Context, trace.Span, func(error)) {
	ctx, span := p.tel.StartAttempt(ctx, op, p.name, modelName)
	target := p.target(modelName)
	p.tel.AddTargetInflight(target, 1)
	start := p.clk.Now()
	var once sync.Once
	return ctx, span, func(err error) {
		once.Do(func() {
			p.tel.AddTargetInflight(target, -1)
			status := upstreamStatus(err)
			p.tel.ObserveUpstream(p.name, modelName, status, p.clk.Now().Sub(start))
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, status)
			}
			span.End()
		})
	}
}

func (p *tracedProvider) Complete(ctx context.Context, req chat.Request) (chat.Response, error) {
	ctx, span, end := p.begin(ctx, "chat", req.Model)
	capture := p.tel.CaptureContent()
	if capture {
		span.SetAttributes(attribute.String(attrInputMessages, inputMessages(req)))
	}
	resp, err := p.next.Complete(ctx, req)
	if err == nil {
		span.SetAttributes(attribute.String(attrResponseModel, resp.Model),
			attribute.Int(attrInputTokens, resp.Usage.PromptTokens),
			attribute.Int(attrOutputTokens, resp.Usage.CompletionTokens))
		if capture {
			span.SetAttributes(attribute.String(attrOutputMessages, outputMessages(resp)))
		}
	}
	end(err)
	return resp, err //nolint:wrapcheck // decorator: errors pass through unchanged for dispatch
}

func (p *tracedProvider) Stream(ctx context.Context, req chat.Request) (provider.Stream, error) {
	ctx, span, end := p.begin(ctx, "chat", req.Model)
	var content *streamContent
	if p.tel.CaptureContent() {
		span.SetAttributes(attribute.String(attrInputMessages, inputMessages(req)))
		content = &streamContent{}
	}
	s, err := p.next.Stream(ctx, req)
	if err != nil {
		end(err)
		return nil, err //nolint:wrapcheck // decorator: errors pass through unchanged for dispatch
	}
	return &tracedStream{next: s, span: span, end: end, content: content}, nil
}

func (p *tracedProvider) Embed(ctx context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error) {
	ctx, span, end := p.begin(ctx, "embeddings", req.Model)
	resp, err := p.next.Embed(ctx, req)
	if err == nil {
		span.SetAttributes(attribute.String(attrResponseModel, resp.Model),
			attribute.Int(attrInputTokens, resp.Usage.PromptTokens))
	}
	end(err)
	return resp, err //nolint:wrapcheck // decorator: errors pass through unchanged
}

func (p *tracedProvider) Models(ctx context.Context) ([]string, error) {
	return p.next.Models(ctx) //nolint:wrapcheck // decorator: errors pass through unchanged
}

type tracedStream struct {
	next    provider.Stream
	span    trace.Span
	end     func(error)
	model   string
	usage   *chat.Usage
	content *streamContent
}

func (s *tracedStream) Recv() (chat.Chunk, error) {
	c, err := s.next.Recv()
	switch {
	case err == nil:
		if s.content != nil {
			s.content.add(c)
		}
		if s.model == "" && c.Model != "" {
			s.model = c.Model
			s.span.SetAttributes(attribute.String(attrResponseModel, c.Model))
		}
		if c.Usage != nil {
			u := *c.Usage
			s.usage = &u
		}
	case errors.Is(err, io.EOF):
		s.finish(nil)
	default:
		s.finish(err)
	}
	return c, err //nolint:wrapcheck // decorator: io.EOF must stay unwrapped
}

func (s *tracedStream) finish(err error) {
	if s.content != nil {
		s.content.set(s.span)
		s.content = nil
	}
	if s.usage != nil {
		s.span.SetAttributes(attribute.Int(attrInputTokens, s.usage.PromptTokens),
			attribute.Int(attrOutputTokens, s.usage.CompletionTokens))
	}
	s.end(err)
}

func (s *tracedStream) Close() error {
	err := s.next.Close()
	s.finish(nil)
	return err //nolint:wrapcheck // decorator
}

func upstreamStatus(err error) string {
	if err == nil {
		return "ok"
	}
	if code := provider.StatusCode(err); code > 0 {
		return strconv.Itoa(code)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, provider.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, provider.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, provider.ErrContextLength):
		return "context_length"
	case errors.Is(err, provider.ErrBadRequest):
		return "bad_request"
	}
	return "unavailable"
}
