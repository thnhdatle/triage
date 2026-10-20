// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"strings"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const (
	phoneConfContext = 0.85
	phoneConfE164    = 0.85
	phoneMinDigits   = 8
	e164MaxDigits    = 15
)

var (
	phoneE164Re = newRunMatcher(`\+[1-9][\d ().-]{6,20}\d`, '+')
	phoneKRRe   = newRunMatcher(`0(?:1[016-9]|2|[3-6][1-5]|70)[ .-]?\d{3,4}[ .-]?\d{4}`, 0)
	phoneVNRe   = newRunMatcher(
		`0(?:3[2-9]|5[25689]|7[06-9]|8[1-9]|9\d)\d[ .-]?\d{3}[ .-]?\d{3}|02\d[ .-]?\d{4}[ .-]?\d{4}`, 0)
	phoneUSRe = newRunMatcher(`(?:\+?1[ .-]?)?(?:\(\d{3}\)|\d{3})[ .-]?\d{3}[ .-]?\d{4}`, 0)
)

func newPhone() *rule {
	return &rule{
		id:    TypePhone,
		class: classContext,
		gate:  func(p *profile) bool { return p.digits >= phoneMinDigits },
		scan:  scanPhone,
	}
}

func scanPhone(p *profile) []privacy.Finding {
	var cands []candidate
	add := func(s, e int, conf float64) {
		cands = append(cands, candidate{newFinding(p, TypePhone, s, e, conf), classContext})
	}
	if strings.IndexByte(p.text, '+') >= 0 {
		for _, m := range phoneE164Re.findAll(p.text) {
			if e, ok := validE164(p.text, m[0], m[1]); ok {
				add(m[0], e, phoneConfE164)
			}
		}
	}
	local := [...]struct {
		re    *runMatcher
		valid func(digits string) bool
	}{
		{phoneKRRe, func(string) bool { return true }},
		{phoneVNRe, func(string) bool { return true }},
		{phoneUSRe, validUSPhone},
	}
	for _, l := range local {
		for _, m := range l.re.findAll(p.text) {
			s, e := m[0], m[1]
			if l.valid(digitsOf(p.text[s:e])) && isolated(p.text, s, e) && hasContext(phoneContext, p.text, s, e) {
				add(s, e, phoneConfContext)
			}
		}
	}
	return resolve(cands)
}

// validE164 trims a '+' candidate to at most 15 digits and checks its edges.
// A leading '+' is context enough.
func validE164(text string, s, e int) (int, bool) {
	if s > 0 && (isAlnum(text[s-1]) || text[s-1] == ')') {
		return 0, false
	}
	for len(digitsOf(text[s:e])) > e164MaxDigits {
		i := strings.LastIndexAny(text[s:e], " .-")
		if i < 0 {
			return 0, false
		}
		e = s + strings.LastIndexAny(text[s:s+i], "0123456789") + 1
	}
	if len(digitsOf(text[s:e])) < phoneMinDigits || !isolated(text, s, e) {
		return 0, false
	}
	return e, true
}

// validUSPhone checks NANP rules: area code and exchange start with 2-9.
func validUSPhone(d string) bool {
	if len(d) == 11 {
		if d[0] != '1' {
			return false
		}
		d = d[1:]
	}
	return len(d) == 10 && d[0] >= '2' && d[3] >= '2'
}
