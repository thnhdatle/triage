// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
)

type sseSink struct {
	s       *Server
	w       http.ResponseWriter
	rc      *http.ResponseController
	id      string
	route   string
	usage   bool
	created int64

	started   bool
	errorSent bool
}

func newSSESink(s *Server, w http.ResponseWriter, req chat.Request) *sseSink {
	return &sseSink{
		s: s, w: w, rc: http.NewResponseController(w), id: req.ID, route: req.Model, usage: req.IncludeUsage,
		created: s.clk.Now().Unix(),
	}
}

func (k *sseSink) Start(t model.Target) error {
	if k.started {
		return nil
	}
	k.started = true
	h := k.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set(HeaderDecision, k.id)
	h.Set(HeaderRoute, k.route)
	if t.Name != "" {
		h.Set(HeaderTarget, t.Name)
	}
	k.w.WriteHeader(http.StatusOK)
	return k.flush()
}

func (k *sseSink) Chunk(c chat.Chunk) error {
	if !k.usage && len(c.Choices) == 0 && c.Usage != nil {
		return nil
	}
	b, err := json.Marshal(encodeChunk(c, "chatcmpl-"+k.id, k.created, k.usage))
	if err != nil {
		return fmt.Errorf("encode chunk: %w", err)
	}
	return k.event("data: " + string(b))
}

func (k *sseSink) Comment(text string) error {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	return k.event(": " + text)
}

func (k *sseSink) Error(err error) error {
	if !k.started {
		return nil
	}
	k.errorSent = true
	b, mErr := json.Marshal(fromError(err).body())
	if mErr != nil {
		return fmt.Errorf("encode error event: %w", mErr)
	}
	return k.event("data: " + string(b))
}

func (k *sseSink) finish(out route.Outcome, err error) {
	switch {
	case err != nil && !k.started:
		k.s.routeError(k.w, out, err)
	case err != nil:
	default:
		if !k.started {
			_ = k.Start(model.Target{Name: out.Target})
		}
		_ = k.event("data: [DONE]")
	}
}

func (k *sseSink) event(line string) error {
	if err := k.rc.SetWriteDeadline(k.s.deadline()); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := k.w.Write([]byte(line + "\n\n")); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return k.flush()
}

func (k *sseSink) flush() error {
	if err := k.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}
