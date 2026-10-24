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

const ID = "piiregex"

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

func IDs() []string {
	return []string{
		TypeCreditCard, TypeIBAN, TypeEmail, TypeIP, TypeUSSSN, TypeSecret, TypeKRRRN, TypePhone, TypeVNCCCD,
		TypeStreetAddress,
	}
}

func WithDisabled(ids ...string) Option {
	return func(c *config) { c.disabled = append(c.disabled, ids...) }
}

type Option func(*config)

func WithStreetAddress(enabled bool) Option {
	return func(c *config) { c.streetAddress = enabled }
}

// Detector is the composite regex PII detector.
type Detector struct {
	rules []*rule
}

var _ privacy.Detector = (*Detector)(nil)

// New returns a Detector with all rules enabled except the ones the options turn off.
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

func (d *Detector) ID() string { return ID }

func (d *Detector) Detectors() []privacy.Detector {
	out := make([]privacy.Detector, len(d.rules))
	for i, r := range d.rules {
		out[i] = r
	}
	return out
}

// Detect returns the PII findings in text.
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

type candidate struct {
	privacy.Finding
	class class
}

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
