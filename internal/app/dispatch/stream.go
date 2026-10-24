// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const UnmaskPartialComment = "triage-unmask=partial"

type relay struct {
	r    *run
	sink Sink

	started      bool
	firstID      string
	firstModel   string
	firstCreated int64
	role         chat.Role
	produced     strings.Builder
	toolCall     bool
	multiChoice  bool
	finished     bool
	anyMasked    bool
	partial      bool
}

type streamAttempt struct {
	attempt
	su      *privacy.StreamUnmasker
	relayed bool
}

type outcome int

const (
	outcomeRetry outcome = iota
	outcomeDone
	outcomeFail
)

// Stream runs the plan's chain and relays the chunks to sink.
func (d *Dispatcher) Stream(ctx context.Context, plan Plan, sink Sink) (Result, error) {
	r := d.newRun(plan)
	ctx, stop := watch(ctx, d.clk, d.opts.TotalTimeout, ErrTotalTimeout)
	defer stop()
	s := &relay{r: r, sink: sink}
	for {
		t, ok := r.pick(ctx)
		if !ok {
			return r.res, s.fail(ctx, r.failure(ctx, s.started))
		}
		switch out, err := s.attempt(ctx, t); out {
		case outcomeDone:
			if err := s.finish(t); err != nil {
				return r.res, r.wrap(err, true)
			}
			return r.res, nil
		case outcomeFail:
			if err != nil {
				return r.res, r.wrap(err, s.started)
			}
			return r.res, s.fail(ctx, r.failure(ctx, s.started))
		case outcomeRetry:
		}
	}
}

func (r *run) wrap(err error, started bool) *Error {
	return &Error{Last: err, Attempts: append([]decision.Attempt(nil), r.res.Attempts...), Started: started}
}

func (s *relay) fail(ctx context.Context, e *Error) *Error {
	if s.started && (ctx.Err() == nil || errors.Is(e.Last, ErrTotalTimeout)) {
		_ = s.sink.Error(e.Last)
	}
	return e
}

func (s *relay) attempt(ctx context.Context, t model.Target) (outcome, error) {
	r := s.r
	a := &streamAttempt{attempt: attempt{target: t, start: r.d.clk.Now(), continuation: s.started}}
	req, masked, err := r.prepare(ctx, t, s.started, s.produced.String())
	a.masked = masked
	if err != nil {
		return s.refused(a, err)
	}
	req.Stream = true
	req.IncludeUsage = true
	if masked {
		a.su = r.mapping.NewStreamUnmasker()
	}
	r.begin(t)
	up := startUpstream(ctx, r.d.providers[t.Provider], req)
	attErr, sinkErr := s.pump(ctx, up, a)
	up.stop()
	if sinkErr == nil && attErr != nil && s.started && a.su != nil {
		sinkErr = s.flush(a)
	}
	if a.su != nil && a.relayed {
		s.anyMasked = true
		s.partial = s.partial || a.su.Partial()
	}
	if attErr != nil && s.finished && !s.multiChoice {
		attErr = nil
	}
	return s.settle(ctx, a, attErr, sinkErr)
}

func (s *relay) refused(a *streamAttempt, err error) (outcome, error) {
	a.err = err
	s.r.refuse(a.attempt)
	return outcomeRetry, nil
}

func (s *relay) settle(ctx context.Context, a *streamAttempt, attErr, sinkErr error) (outcome, error) {
	r := s.r
	a.err = attErr
	if attErr != nil && a.relayed && ctx.Err() == nil {
		a.status = statusCut
	}
	r.record(a.attempt)
	r.report(ctx, a.target, attErr)
	switch {
	case sinkErr != nil:
		return outcomeFail, sinkErr
	case attErr == nil:
		return outcomeDone, nil
	}
	r.failed(a.target, attErr)
	if s.started && (r.d.opts.StreamFailure == StreamFailureError || s.toolCall || s.multiChoice) {
		return outcomeFail, nil
	}
	return outcomeRetry, nil
}

func (s *relay) pump(ctx context.Context, up *upstream, a *streamAttempt) (attErr, sinkErr error) {
	timeout := s.r.d.attemptTimeout(a.target)
	for {
		c, err := up.next(ctx, s.r.d.clk, timeout)
		if errors.Is(err, io.EOF) {
			if a.su != nil {
				return nil, s.flush(a)
			}
			return nil, nil
		}
		if err != nil {
			if cerr := ctxErr(ctx); cerr != nil {
				err = cerr
			}
			return err, nil
		}
		if c.Usage != nil {
			a.usage = *c.Usage
			c.Usage = nil
		}
		if len(c.Choices) == 0 {
			continue
		}
		if a.su != nil {
			c = a.su.UnmaskChunk(c)
		}
		if err := s.emit(a, c); err != nil {
			return nil, err
		}
	}
}

func (s *relay) flush(a *streamAttempt) error {
	for _, p := range a.su.Flush() {
		ch := chat.ChunkChoice{Index: p.Choice}
		if p.ToolCall == privacy.ContentStream {
			ch.Delta.Content = p.Text
		} else {
			ch.Delta.ToolCalls = []chat.ToolCallDelta{{Index: p.ToolCall, Arguments: p.Text}}
		}
		if err := s.emit(a, chat.Chunk{Choices: []chat.ChunkChoice{ch}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *relay) emit(a *streamAttempt, c chat.Chunk) error {
	if !hasPayload(c) {
		if !s.started {
			for _, ch := range c.Choices {
				if ch.Delta.Role != "" {
					s.role = ch.Delta.Role
				}
			}
		}
		return nil
	}
	if !s.started {
		s.started = true
		s.firstID, s.firstModel, s.firstCreated = c.ID, c.Model, c.Created
		if err := s.sink.Start(a.target); err != nil {
			return err
		}
	}
	out := chat.Chunk{ID: s.firstID, Model: s.firstModel, Created: s.firstCreated}
	out.Choices = make([]chat.ChunkChoice, len(c.Choices))
	for i, ch := range c.Choices {
		switch {
		case a.continuation:
			ch.Delta.Role = ""
		case !a.relayed && i == 0 && ch.Delta.Role == "":
			ch.Delta.Role = s.role
		}
		s.observe(ch)
		out.Choices[i] = ch
	}
	a.relayed = true
	return s.sink.Chunk(out)
}

func (s *relay) observe(ch chat.ChunkChoice) {
	if ch.Index != 0 {
		s.multiChoice = true
		return
	}
	s.produced.WriteString(ch.Delta.Content)
	if len(ch.Delta.ToolCalls) > 0 {
		s.toolCall = true
	}
	if ch.FinishReason != "" {
		s.finished = true
	}
}

func hasPayload(c chat.Chunk) bool {
	for _, ch := range c.Choices {
		if ch.Delta.Content != "" || len(ch.Delta.ToolCalls) > 0 || ch.FinishReason != "" {
			return true
		}
	}
	return false
}

func (s *relay) finish(t model.Target) error {
	r := s.r
	if !s.started {
		if err := s.startEmpty(t); err != nil {
			return err
		}
	}
	r.res.Target = t
	r.res.Model = s.firstModel
	if s.anyMasked {
		r.res.Unmask = decision.UnmaskOK
	}
	if r.plan.Request.IncludeUsage {
		u := r.res.Usage
		c := chat.Chunk{
			ID: s.firstID, Model: s.firstModel, Created: s.firstCreated,
			Choices: []chat.ChunkChoice{}, Usage: &u,
		}
		if err := s.sink.Chunk(c); err != nil {
			return err
		}
	}
	if s.partial {
		r.res.Unmask = decision.UnmaskPartial
		return s.sink.Comment(UnmaskPartialComment)
	}
	return nil
}

func (s *relay) startEmpty(t model.Target) error {
	s.started = true
	s.firstModel = t.Model
	if err := s.sink.Start(t); err != nil {
		return err
	}
	if s.role == "" {
		return nil
	}
	return s.sink.Chunk(chat.Chunk{Model: s.firstModel, Choices: []chat.ChunkChoice{{Delta: chat.Delta{Role: s.role}}}})
}
