// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"regexp"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// regexDetector is a tiny fake Detector: every match of re is a finding of
// type typ. With noValue it reports spans only, so Scan must fill Value.
type regexDetector struct {
	id      string
	typ     string
	re      *regexp.Regexp
	noValue bool
}

func (d regexDetector) ID() string { return d.id }

func (d regexDetector) Detect(_ context.Context, text string) ([]Finding, error) {
	locs := d.re.FindAllStringIndex(text, -1)
	out := make([]Finding, 0, len(locs))
	for _, loc := range locs {
		f := Finding{DetectorID: d.id, Type: d.typ, Start: loc[0], End: loc[1], Confidence: 1}
		if !d.noValue {
			f.Value = text[loc[0]:loc[1]]
		}
		out = append(out, f)
	}
	return out, nil
}

func emailDetector() regexDetector {
	return regexDetector{id: "email", typ: "email", re: regexp.MustCompile(`[a-z0-9.]+@[a-z0-9.]+\.[a-z]+`)}
}

func phoneDetector() regexDetector {
	return regexDetector{id: "phone", typ: "phone", re: regexp.MustCompile(`\+[0-9]{6,12}`)}
}

// staticDetector returns fixed findings or a fixed error.
type staticDetector struct {
	id       string
	findings []Finding
	err      error
}

func (d staticDetector) ID() string { return d.id }

func (d staticDetector) Detect(context.Context, string) ([]Finding, error) {
	return d.findings, d.err
}

// recordingDetector records every text it is asked to scan.
type recordingDetector struct {
	mu    sync.Mutex
	texts []string
}

func (d *recordingDetector) ID() string { return "rec" }

func (d *recordingDetector) Detect(_ context.Context, text string) ([]Finding, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.texts = append(d.texts, text)
	return nil, nil
}

func userReq(texts ...string) chat.Request {
	req := chat.Request{}
	for _, t := range texts {
		req.Messages = append(req.Messages, chat.TextMessage(chat.RoleUser, t))
	}
	return req
}

func finding(typ, value string) Finding {
	return Finding{DetectorID: typ, Type: typ, Value: value}
}

func toolCallMsg(args ...string) chat.Message {
	msg := chat.Message{Role: chat.RoleAssistant}
	for _, a := range args {
		msg.ToolCalls = append(msg.ToolCalls, chat.ToolCall{
			ID: "call", Type: "function", Function: chat.FunctionCall{Name: "f", Arguments: a},
		})
	}
	return msg
}
