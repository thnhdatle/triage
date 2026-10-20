// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"context"
	"fmt"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// class orders detectors for overlap resolution: a higher class wins.
type class int

const (
	classHeuristic class = iota
	classContext
	classStructural
	classChecksum
)

// profile holds the cheap byte-level facts about a text that the literal
// prefilter uses to skip rules that cannot match.
type profile struct {
	text   string
	digits int
	at     bool
	colon  bool
	dot    bool
}

func newProfile(text string) profile {
	p := profile{text: text}
	for i := range len(text) {
		switch c := text[i]; {
		case c >= '0' && c <= '9':
			p.digits++
		case c == '@':
			p.at = true
		case c == ':':
			p.colon = true
		case c == '.':
			p.dot = true
		}
	}
	return p
}

// rule is one sub-detector. It implements privacy.Detector on its own so
// precision and recall can be measured per detector.
type rule struct {
	id    string
	class class
	// gate is the literal prefilter: false means the scan cannot match.
	gate func(p *profile) bool
	scan func(p *profile) []privacy.Finding
}

// ID returns the sub-detector id, which equals the finding type it produces.
func (r *rule) ID() string { return r.id }

// Detect scans text with this sub-detector only.
func (r *rule) Detect(ctx context.Context, text string) ([]privacy.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("piiregex: %s: %w", r.id, err)
	}
	p := newProfile(text)
	return r.run(&p), nil
}

func (r *rule) run(p *profile) []privacy.Finding {
	if !r.gate(p) {
		return nil
	}
	return r.scan(p)
}

func newFinding(p *profile, id string, start, end int, conf float64) privacy.Finding {
	return privacy.Finding{
		DetectorID: id,
		Type:       id,
		Start:      start,
		End:        end,
		Confidence: conf,
		Value:      p.text[start:end],
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isAlnum(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isNumSep(b byte) bool { return b == '-' || b == '.' || b == '/' }

// isolated reports whether text[s:e] is not glued to further digits, either
// directly or across one '-', '.' or '/'.
func isolated(text string, s, e int) bool {
	if s > 0 {
		if isDigit(text[s-1]) || (s > 1 && isNumSep(text[s-1]) && isDigit(text[s-2])) {
			return false
		}
	}
	if e < len(text) {
		if isDigit(text[e]) || (e+1 < len(text) && isNumSep(text[e]) && isDigit(text[e+1])) {
			return false
		}
	}
	return true
}

// digitsOf returns the ASCII digits of s.
func digitsOf(s string) string {
	b := make([]byte, 0, len(s))
	for i := range len(s) {
		if isDigit(s[i]) {
			b = append(b, s[i])
		}
	}
	return string(b)
}

func atoi(s string) int {
	n := 0
	for i := range len(s) {
		n = n*10 + int(s[i]-'0')
	}
	return n
}
