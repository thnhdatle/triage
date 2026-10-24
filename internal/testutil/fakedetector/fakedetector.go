// SPDX-License-Identifier: Apache-2.0

// Package fakedetector provides a fake privacy.Detector for tests.
package fakedetector

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// Detector is a fake privacy.Detector that records the texts it scanned.
type Detector struct {
	id       string
	typ      string
	literals []string
	re       *regexp.Regexp

	mu    sync.Mutex
	err   error
	texts []string
}

func NewLiteral(id, typ string, literals ...string) *Detector {
	return &Detector{id: id, typ: typ, literals: append([]string(nil), literals...)}
}

func NewRegexp(id, typ string, re *regexp.Regexp) *Detector {
	return &Detector{id: id, typ: typ, re: re}
}

func (d *Detector) WithError(err error) *Detector {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
	return d
}

func (d *Detector) ID() string { return d.id }

func (d *Detector) Detect(ctx context.Context, text string) ([]privacy.Finding, error) {
	d.mu.Lock()
	d.texts = append(d.texts, text)
	err := d.err
	d.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fakedetector: %w", err)
	}
	var findings []privacy.Finding
	add := func(start, end int) {
		findings = append(findings, privacy.Finding{
			DetectorID: d.id,
			Type:       d.typ,
			Start:      start,
			End:        end,
			Confidence: 1,
			Value:      text[start:end],
		})
	}
	for _, lit := range d.literals {
		if lit == "" {
			continue
		}
		for off := 0; ; {
			i := strings.Index(text[off:], lit)
			if i < 0 {
				break
			}
			add(off+i, off+i+len(lit))
			off += i + len(lit)
		}
	}
	if d.re != nil {
		for _, m := range d.re.FindAllStringIndex(text, -1) {
			if m[1] > m[0] {
				add(m[0], m[1])
			}
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Start != findings[j].Start {
			return findings[i].Start < findings[j].Start
		}
		return findings[i].End > findings[j].End
	})
	return findings, nil
}

func (d *Detector) Texts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.texts...)
}

func (d *Detector) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.texts)
}
