// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"fmt"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

const DefaultMaxScanBytes = 256 << 10

type OversizeAction string

const (
	OversizeLocalOnly OversizeAction = "local_only"
	OversizeReject    OversizeAction = "reject"
)

func ParseOversizeAction(s string) (OversizeAction, error) {
	switch a := OversizeAction(s); a {
	case OversizeLocalOnly, OversizeReject:
		return a, nil
	}
	return "", fmt.Errorf("privacy: unknown on_oversize %q (want local_only or reject)", s)
}

type ScanOptions struct {
	ScanSystem   bool
	MaxScanBytes int
	OnOversize   OversizeAction
}

type Result struct {
	Findings []Finding
	Counts   map[string]int
	Oversize bool
}

func (r Result) HasFindings() bool { return len(r.Findings) > 0 || r.Oversize }

func (r Result) Constrained() bool { return r.Oversize || r.Counts[TypeNERUnavailable] > 0 }

func (r Result) RequiresLocal(mode Mode) bool {
	switch mode {
	case ModeLocalOnly:
		return r.HasFindings()
	case ModeMask:
		return r.Constrained()
	case ModeOff:
	}
	return false
}

// Scan runs the detectors over the request texts.
func Scan(ctx context.Context, detectors []Detector, req chat.Request, opts ScanOptions) (Result, error) {
	texts := scopedTexts(req, opts.ScanSystem)
	limit := opts.MaxScanBytes
	if limit <= 0 {
		limit = DefaultMaxScanBytes
	}
	total := 0
	for _, t := range texts {
		total += len(t)
	}
	res := Result{Counts: map[string]int{}}
	if total > limit {
		res.Oversize = true
		if opts.OnOversize == OversizeReject {
			return res, fmt.Errorf("%w (%d > %d bytes)", ErrScanLimit, total, limit)
		}
		return res, nil
	}
	for _, t := range texts {
		for _, d := range detectors {
			fs, err := detect(ctx, d, t)
			if err != nil {
				return Result{Counts: map[string]int{}}, err
			}
			for _, f := range fs {
				res.Findings = append(res.Findings, f)
				res.Counts[f.Type]++
			}
		}
	}
	return res, nil
}

func detect(ctx context.Context, d Detector, text string) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("privacy: detector %s: %w", d.ID(), err)
	}
	fs, err := d.Detect(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("privacy: detector %s: %w", d.ID(), err)
	}
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		if f.Value == "" && f.Type != TypeNERUnavailable {
			if f.Start < 0 || f.End <= f.Start || f.End > len(text) {
				return nil, fmt.Errorf("privacy: detector %s: %s finding has no value and span [%d,%d) is invalid",
					d.ID(), f.Type, f.Start, f.End)
			}
			f.Value = text[f.Start:f.End]
		}
		out = append(out, f)
	}
	return out, nil
}

func scopedTexts(req chat.Request, scanSystem bool) []string {
	var texts []string
	for _, m := range req.Messages {
		if m.Role == chat.RoleSystem && !scanSystem {
			continue
		}
		texts = appendMessageTexts(texts, m)
	}
	return texts
}

func allTexts(req chat.Request) []string {
	var texts []string
	for _, m := range req.Messages {
		texts = appendMessageTexts(texts, m)
	}
	return texts
}

func appendMessageTexts(texts []string, m chat.Message) []string {
	for _, p := range m.Content {
		if p.Type == chat.PartText && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	for _, tc := range m.ToolCalls {
		if tc.Function.Arguments != "" {
			texts = append(texts, tc.Function.Arguments)
		}
	}
	return texts
}
