// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"errors"
	"fmt"
)

// Mode is a route's privacy mode. Strictness order: off < mask < local_only.
type Mode string

// Privacy modes.
const (
	ModeOff       Mode = "off"
	ModeMask      Mode = "mask"
	ModeLocalOnly Mode = "local_only"
)

// ParseMode parses a mode name.
func ParseMode(s string) (Mode, error) {
	m := Mode(s)
	if m.strictness() < 0 {
		return "", fmt.Errorf("privacy: unknown mode %q (want off, mask or local_only)", s)
	}
	return m, nil
}

func (m Mode) strictness() int {
	switch m {
	case ModeOff:
		return 0
	case ModeMask:
		return 1
	case ModeLocalOnly:
		return 2
	}
	return -1
}

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m.strictness() >= 0 }

// StricterThan reports whether m is strictly stricter than o.
func (m Mode) StricterThan(o Mode) bool { return m.strictness() > o.strictness() }

// Finding types that are not produced by a format detector.
const (
	// TypeNERUnavailable marks text the NER detector could not scan (too long,
	// error, timeout). It is fail-closed: it forces local_only in mask mode.
	TypeNERUnavailable = "ner_unavailable"
)

// Finding is a detected PII span in one scanned text. Value is held in memory
// for the request only and is never logged or recorded.
type Finding struct {
	// DetectorID identifies the detector, e.g. "email" or "ner".
	DetectorID string
	// Type is the placeholder type, lower case, e.g. "email", "credit_card".
	Type string
	// Start and End are the byte range of Value in the scanned text.
	Start, End int
	Confidence float64
	Value      string
}

// Detector finds PII in a text. Implementations must be safe for concurrent use.
type Detector interface {
	ID() string
	Detect(ctx context.Context, text string) ([]Finding, error)
}

// Domain errors of the privacy stage (codes in docs/guides/errors.md).
var (
	// ErrScanLimit is returned when the scoped text exceeds max_scan_bytes and
	// on_oversize is reject (413 privacy_scan_limit).
	ErrScanLimit = errors.New("privacy: scoped text exceeds max_scan_bytes")
	// ErrModeNotAllowed is returned when the client asks for a mode stricter
	// than the route's max_client_mode (400 privacy_mode_not_allowed).
	ErrModeNotAllowed = errors.New("privacy: requested mode not allowed on this route")
	// ErrNoLocalTarget is returned when findings restrict a request to local
	// targets and none is eligible (503 privacy_no_local_target).
	ErrNoLocalTarget = errors.New("privacy: no eligible local target")
	// ErrRescanIncomplete is returned by Mapping.Rescan when a detector could
	// not scan the continuation text (ner_unavailable); the text must not be
	// sent to a cloud target.
	ErrRescanIncomplete = errors.New("privacy: continuation text could not be fully scanned")
)
