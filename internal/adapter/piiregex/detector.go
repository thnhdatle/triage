// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// ID is the identifier of the composite detector.
const ID = "piiregex"

// Finding types (and sub-detector ids) produced by this package.
const (
	TypeCreditCard    = "credit_card"
	TypeIBAN          = "iban"
	TypeEmail         = "email"
	TypeIP            = "ip"
	TypeUSSSN         = "us_ssn"
	TypeSecret        = "secret"
	TypeKRRRN         = "kr_rrn"
	TypePhone         = "phone"
	TypeVNCCCD        = "vn_cccd"
	TypeStreetAddress = "street_address"
)

type config struct {
	streetAddress bool
	disabled      []string
}

// IDs returns every detector id, in scan order. street_address is last and is
// disabled unless WithStreetAddress enables it.
func IDs() []string {
	return []string{
		TypeCreditCard, TypeIBAN, TypeEmail, TypeIP, TypeUSSSN, TypeSecret, TypeKRRRN, TypePhone, TypeVNCCCD,
		TypeStreetAddress,
	}
}

// WithDisabled removes the named detectors from the composite. New fails for
// an id that is not in IDs.
func WithDisabled(ids ...string) Option {
	return func(c *config) { c.disabled = append(c.disabled, ids...) }
}

// Option configures New.
type Option func(*config)

// WithStreetAddress enables or disables the street-address heuristic, which is
// disabled by default.
func WithStreetAddress(enabled bool) Option {
	return func(c *config) { c.streetAddress = enabled }
}

// Detector is the composite regex, checksum and structural PII detector. It is
// immutable after New and safe for concurrent use.
type Detector struct {
	rules []*rule
}

var _ privacy.Detector = (*Detector)(nil)

// New builds the composite detector.
func New(opts ...Option) (*Detector, error) {
	var c config
	for _, o := range opts {
		o(&c)
	}
	for _, id := range c.disabled {
		if !slices.Contains(IDs(), id) {
			return nil, fmt.Errorf("piiregex: unknown detector %q (valid: %s)", id, strings.Join(IDs(), ", "))
		}
	}
	rules := []*rule{
		newCreditCard(), newIBAN(), newEmail(), newIP(), newSSN(), newSecret(), newRRN(), newPhone(), newCCCD(),
	}
	if c.streetAddress {
		rules = append(rules, newStreet())
	}
	d := &Detector{}
	for _, r := range rules {
		if !slices.Contains(c.disabled, r.id) {
			d.rules = append(d.rules, r)
		}
	}
	return d, nil
}

// ID returns "piiregex".
func (d *Detector) ID() string { return ID }

// Detectors returns the sub-detectors in scan order. Each reports its own id
// in Finding.DetectorID, which allows per-detector precision and recall.
func (d *Detector) Detectors() []privacy.Detector {
	out := make([]privacy.Detector, len(d.rules))
	for i, r := range d.rules {
		out[i] = r
	}
	return out
}

// Detect scans text with every sub-detector and returns the non-overlapping
// findings ordered by start offset. Overlaps are resolved in favour of the
// higher class (checksum, structural, context, heuristic), then the longer
// span. It returns early with a wrapped context error when ctx is cancelled.
func (d *Detector) Detect(ctx context.Context, text string) ([]privacy.Finding, error) {
	p := newProfile(text)
	var cands []candidate
	for _, r := range d.rules {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("piiregex: scan cancelled: %w", err)
		}
		for _, f := range r.run(&p) {
			cands = append(cands, candidate{f, r.class})
		}
	}
	return resolve(cands), nil
}

// candidate is a finding with the class of the detector that produced it.
type candidate struct {
	privacy.Finding
	class class
}

// resolve drops overlapping candidates deterministically and returns the
// survivors ordered by start offset.
func resolve(cands []candidate) []privacy.Finding {
	if len(cands) == 0 {
		return nil
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		return cmp.Or(
			cmp.Compare(b.class, a.class),
			cmp.Compare(b.End-b.Start, a.End-a.Start),
			cmp.Compare(a.Start, b.Start),
			cmp.Compare(a.Type, b.Type),
		)
	})
	kept := make([]privacy.Finding, 0, len(cands))
	for _, c := range cands {
		if !slices.ContainsFunc(kept, func(k privacy.Finding) bool { return c.Start < k.End && k.Start < c.End }) {
			kept = append(kept, c.Finding)
		}
	}
	slices.SortFunc(kept, func(a, b privacy.Finding) int {
		return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End))
	})
	return kept
}
