// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"fmt"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// DefaultMaxScanBytes is the default privacy.max_scan_bytes (256 KiB).
const DefaultMaxScanBytes = 256 << 10

// OversizeAction is what happens when the scoped text exceeds MaxScanBytes
// (privacy.on_oversize).
type OversizeAction string

// Oversize actions.
const (
	// OversizeLocalOnly treats the request as having findings, so only local
	// targets are eligible, in mask mode too. It is the default.
	OversizeLocalOnly OversizeAction = "local_only"
	// OversizeReject rejects the request with ErrScanLimit (413).
	OversizeReject OversizeAction = "reject"
)

// ParseOversizeAction parses a privacy.on_oversize value.
func ParseOversizeAction(s string) (OversizeAction, error) {
	switch a := OversizeAction(s); a {
	case OversizeLocalOnly, OversizeReject:
		return a, nil
	}
	return "", fmt.Errorf("privacy: unknown on_oversize %q (want local_only or reject)", s)
}

// ScanOptions configures Scan.
type ScanOptions struct {
	// ScanSystem includes system messages in the scope (privacy.scan_system).
	ScanSystem bool
	// MaxScanBytes is the scope limit; values <= 0 mean DefaultMaxScanBytes.
	MaxScanBytes int
	// OnOversize applies when the scope exceeds MaxScanBytes. Any value other
	// than OversizeReject, including "", behaves as OversizeLocalOnly.
	OnOversize OversizeAction
}

// Result is the outcome of a request scan. Findings hold values in memory
// only; Counts (by type) is what decisions, metrics and spans may record.
type Result struct {
	// Findings are in scan order: message, then text parts before tool-call
	// arguments, then detector order. Every finding except ner_unavailable has
	// a non-empty Value.
	Findings []Finding
	// Counts is the number of findings per type, never nil.
	Counts map[string]int
	// Oversize reports that the scope exceeded MaxScanBytes and nothing ran.
	Oversize bool
}

// HasFindings reports whether the request counts as having findings: any
// finding (ner_unavailable included) or an oversize scope. In local_only mode
// this restricts every attempt to local targets.
func (r Result) HasFindings() bool { return len(r.Findings) > 0 || r.Oversize }

// Constrained reports whether the result forces local-only routing even in
// mask mode: the scope was oversize or a detector could not scan
// (ner_unavailable). Ordinary findings do not constrain mask mode; they are
// masked for cloud attempts instead. For local_only mode use HasFindings, or
// RequiresLocal for both.
func (r Result) Constrained() bool { return r.Oversize || r.Counts[TypeNERUnavailable] > 0 }

// RequiresLocal reports whether every attempt must go to a local target under
// mode: HasFindings for local_only, Constrained for mask, never for off (where
// findings only steer the mf strategy).
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

// Scan runs the detectors over the scoped text of req: text parts and
// tool-call arguments of every non-system message (user, assistant, tool and
// any other role, fail-closed), plus system messages when opts.ScanSystem.
// Image parts are never scanned. Each scoped text is scanned separately, so
// Finding.Start and End are relative to that text.
//
// If the scope exceeds opts.MaxScanBytes no detector runs and Oversize is set;
// with OversizeReject the error is ErrScanLimit. A detector error, a cancelled
// ctx or a finding without value and valid span is returned wrapped and the
// result is empty; the caller applies its failure policy.
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

// detect runs one detector and fills Value from the span when the detector
// left it empty.
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

// scopedTexts returns the non-empty texts Scan covers, in order.
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

// allTexts returns every non-empty text part and tool-call argument of req,
// system messages included.
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
