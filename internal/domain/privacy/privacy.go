// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"errors"
	"fmt"
)

type Mode string

const (
	ModeOff       Mode = "off"
	ModeMask      Mode = "mask"
	ModeLocalOnly Mode = "local_only"
)

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

func (m Mode) Valid() bool { return m.strictness() >= 0 }

func (m Mode) StricterThan(o Mode) bool { return m.strictness() > o.strictness() }

const (
	TypeNERUnavailable = "ner_unavailable"
)

type Finding struct {
	DetectorID string
	Type       string
	Start, End int
	Confidence float64
	Value      string
}

// Detector finds sensitive values in text.
type Detector interface {
	ID() string
	Detect(ctx context.Context, text string) ([]Finding, error)
}

var (
	ErrScanLimit        = errors.New("privacy: scoped text exceeds max_scan_bytes")
	ErrModeNotAllowed   = errors.New("privacy: requested mode not allowed on this route")
	ErrNoLocalTarget    = errors.New("privacy: no eligible local target")
	ErrRescanIncomplete = errors.New("privacy: continuation text could not be fully scanned")
)
