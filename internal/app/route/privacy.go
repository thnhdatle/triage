// SPDX-License-Identifier: Apache-2.0

package route

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// Privacy reason details (decision reasons "privacy:<what>").
const (
	reasonLocalOnly = "local_only"
	reasonMask      = "mask"
	// reasonScanError is recorded before local_only when a detector failed.
	reasonScanError = "scan_error"
)

// effectiveMode returns the stricter of the route's mode and the client's
// X-Triage-Privacy. A value looser than or equal to the route's mode is
// ignored; a stricter one beyond max_client_mode, or an unknown value, is
// privacy.ErrModeNotAllowed.
func effectiveMode(rp Privacy, headers map[string]string) (privacy.Mode, error) {
	v := strings.ToLower(strings.TrimSpace(headers[HeaderPrivacy]))
	if v == "" {
		return rp.Mode, nil
	}
	m, err := privacy.ParseMode(v)
	if err != nil {
		return "", fmt.Errorf("%w: %s %q is not a privacy mode", privacy.ErrModeNotAllowed, HeaderPrivacy, v)
	}
	if !m.StricterThan(rp.Mode) {
		return rp.Mode, nil
	}
	if m.StricterThan(rp.MaxClientMode) {
		return "", fmt.Errorf("%w: %s %q exceeds max_client_mode %q",
			privacy.ErrModeNotAllowed, HeaderPrivacy, m, rp.MaxClientMode)
	}
	return m, nil
}

// privacyStage sets the effective mode, scans when needed and derives the
// locality constraint and the mask mapping.
func (r *request) privacyStage(ctx context.Context) error {
	mode, err := effectiveMode(r.route.Privacy, r.req.Headers)
	if err != nil {
		return err
	}
	r.mode = mode
	r.dec.Privacy = mode
	steerOnly := mode == privacy.ModeOff
	if steerOnly && r.route.Strategy != StrategyMF {
		return nil
	}
	if err := r.runScan(ctx, r.req, steerOnly); err != nil {
		return err
	}
	if steerOnly {
		// Under off, findings only steer mf (difficulty stage); routing is
		// never constrained.
		return nil
	}
	switch {
	case r.scanError || r.scan.RequiresLocal(mode):
		r.restrictLocal()
	case mode == privacy.ModeMask && len(r.scan.Findings) > 0:
		r.mapping = privacy.NewMapping(r.req, r.scan.Findings)
		r.dec.AddReason(decision.ReasonPrivacy(reasonMask))
	}
	return nil
}

// runScan scans req. A detector error does not fail the request: it is
// handled fail-closed by the caller (scanError). An oversize scope with
// on_oversize reject is privacy.ErrScanLimit, except when steerOnly (mode off
// on an mf route), where oversize only makes mf fall back.
func (r *request) runScan(ctx context.Context, req chat.Request, steerOnly bool) error {
	opts := privacy.ScanOptions{
		ScanSystem:   r.p.opts.Privacy.ScanSystem,
		MaxScanBytes: r.p.opts.Privacy.MaxScanBytes,
		OnOversize:   r.p.opts.Privacy.OnOversize,
	}
	if steerOnly {
		opts.OnOversize = privacy.OversizeLocalOnly
	}
	res, err := privacy.Scan(ctx, r.p.deps.Detectors, req, opts)
	r.scanned = true
	r.scan = res
	r.dec.Findings = maps.Clone(res.Counts)
	r.dec.Oversize = res.Oversize
	if res.Oversize {
		r.p.deps.Observer.Observe(Event{Kind: EventOversize, Route: r.route.Name})
	}
	if len(res.Counts) > 0 {
		r.p.deps.Observer.Observe(Event{Kind: EventFindings, Route: r.route.Name, Counts: maps.Clone(res.Counts)})
	}
	if err != nil {
		if res.Oversize {
			return err
		}
		r.scanError = true
		r.dec.AddReason(decision.ReasonPrivacy(reasonScanError))
	}
	return nil
}

func (r *request) restrictLocal() {
	r.localOnly = true
	r.dec.AddReason(decision.ReasonPrivacy(reasonLocalOnly))
}

// steersMF reports whether the scan forbids sending the scored text to mf's
// external embedding API: any finding, oversize or a failed scan.
func (r *request) steersMF() bool {
	return r.scanError || r.scan.HasFindings()
}

// localities returns the allowed localities of every attempt.
func (r *request) localities() model.LocalitySet {
	if r.localOnly {
		return model.LocalOnly()
	}
	return model.AllLocalities()
}
