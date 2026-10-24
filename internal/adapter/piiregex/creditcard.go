// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const (
	ccMinDigits = 13
	ccMaxDigits = 19
	ccConf      = 0.95
)

var creditCardRe = newRunMatcher(`\d(?:[ -]?\d){12,}`, 0)

func newCreditCard() *rule {
	return &rule{
		id:    TypeCreditCard,
		class: classChecksum,
		gate:  func(p *profile) bool { return p.digits >= ccMinDigits },
		scan:  scanCreditCard,
	}
}

func scanCreditCard(p *profile) []privacy.Finding {
	var out []privacy.Finding
	for _, m := range creditCardRe.findAll(p.text) {
		if s, e, ok := creditCardWindow(p.text, m[0], m[1]); ok {
			out = append(out, newFinding(p, TypeCreditCard, s, e, ccConf))
		}
	}
	return out
}

func creditCardWindow(text string, ms, me int) (start, end int, ok bool) {
	var groups [][2]int
	for i := ms; i < me; {
		if !isDigit(text[i]) {
			i++
			continue
		}
		j := i
		for j < me && isDigit(text[j]) {
			j++
		}
		groups = append(groups, [2]int{i, j})
		i = j
	}
	for i := range groups {
		if w, ok := longestCardFrom(text, groups, i); ok {
			return groups[i][0], w, true
		}
	}
	return 0, 0, false
}

func luhnValid(d string) bool {
	sum := 0
	double := false
	for i := len(d) - 1; i >= 0; i-- {
		v := int(d[i] - '0')
		if double {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
		double = !double
	}
	return sum%10 == 0
}

func plausibleIIN(d string) bool {
	n := len(d)
	if d[0] == '4' {
		return n == 13 || n == 16 || n == 19
	}
	schemes := [...]struct{ digits, lo, hi, minLen, maxLen int }{
		{2, 51, 55, 16, 16},
		{4, 2221, 2720, 16, 16},
		{2, 34, 34, 15, 15},
		{2, 37, 37, 15, 15},
		{4, 6011, 6011, 16, 19},
		{2, 65, 65, 16, 19},
		{3, 644, 649, 16, 19},
		{2, 62, 62, 16, 19},
		{4, 3528, 3589, 16, 19},
		{3, 300, 305, 14, 19},
		{2, 36, 36, 14, 19},
		{2, 38, 39, 14, 19},
		{2, 50, 50, 13, 19},
		{2, 56, 58, 13, 19},
		{2, 60, 60, 13, 19},
		{2, 63, 63, 13, 19},
		{2, 67, 67, 13, 19},
	}
	for _, sc := range schemes {
		if p := atoi(d[:sc.digits]); p >= sc.lo && p <= sc.hi {
			return n >= sc.minLen && n <= sc.maxLen
		}
	}
	return false
}

func cardIsolated(text string, s, e int) bool {
	if !isolated(text, s, e) {
		return false
	}
	if s > 0 && (isAlnum(text[s-1]) || (s > 1 && (text[s-1] == ' ' || text[s-1] == '-') && isDigit(text[s-2]))) {
		return false
	}
	if e >= len(text) {
		return true
	}
	glued := e+1 < len(text) && (text[e] == ' ' || text[e] == '-') && isDigit(text[e+1])
	return !isAlnum(text[e]) && !glued
}

func longestCardFrom(text string, groups [][2]int, i int) (end int, ok bool) {
	total := 0
	for j := i; j < len(groups); j++ {
		total += groups[j][1] - groups[j][0]
		if total > ccMaxDigits {
			break
		}
		if j > i && groups[i][1]-groups[i][0] != 4 {
			break
		}
		s, e := groups[i][0], groups[j][1]
		if total < ccMinDigits {
			continue
		}
		if d := digitsOf(text[s:e]); luhnValid(d) && plausibleIIN(d) && cardIsolated(text, s, e) {
			end, ok = e, true
		}
	}
	return end, ok
}
