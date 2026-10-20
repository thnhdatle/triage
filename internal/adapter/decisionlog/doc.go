// SPDX-License-Identifier: Apache-2.0

// Package decisionlog implements the in-memory ring buffer decision recorder.
//
// Log implements decision.Recorder, keeps the newest decisions up to a fixed
// size (decisions.buffer_size, default 10,000) and serves the admin API: Get
// looks a decision up by id and List pages through them newest first with an
// optional Filter and an opaque cursor. Decisions carry no prompt text or
// finding values, and Log stores deep copies so callers cannot mutate history.
//
//	l, _ := decisionlog.New(decisionlog.DefaultSize)
//	l.Record(decision.Decision{ID: "d1", Route: "triage-auto"})
//	page, _ := l.List(decisionlog.Filter{Route: "triage-auto"}, 50, "")
package decisionlog
