// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"net/netip"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const ipConf = 0.8

var (
	ipv4Re = newRunMatcher(`\d{1,3}(?:\.\d{1,3}){3}`, 0)
	ipv6Re = newRunMatcher(`[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*`, ':')
)

func newIP() *rule {
	return &rule{
		id:    TypeIP,
		class: classStructural,
		gate:  func(p *profile) bool { return (p.digits >= 4 && p.dot) || p.colon },
		scan:  scanIP,
	}
}

func scanIP(p *profile) []privacy.Finding {
	var out []candidate
	add := func(s, e int) {
		out = append(out, candidate{newFinding(p, TypeIP, s, e, ipConf), classStructural})
	}
	if p.digits >= 4 && p.dot {
		for _, m := range ipv4Re.findAll(p.text) {
			if validIPv4(p.text, m[0], m[1]) {
				add(m[0], m[1])
			}
		}
	}
	if p.colon {
		for _, m := range ipv6Re.findAll(p.text) {
			if s, e, ok := validIPv6(p.text, m[0], m[1]); ok {
				add(s, e)
			}
		}
	}
	return resolve(out)
}

func validIPv4(text string, s, e int) bool {
	if s > 0 && (text[s-1] == 'v' || text[s-1] == 'V') {
		return false
	}
	if !isolated(text, s, e) {
		return false
	}
	a, err := netip.ParseAddr(text[s:e])
	return err == nil && a.Is4()
}

func validIPv6(text string, s, e int) (start, end int, ok bool) {
	if s > 0 && (isAlnum(text[s-1]) || text[s-1] == '_') {
		return 0, 0, false
	}
	e = trimIPv6(text, s, e)
	c := text[s:e]
	if strings.Count(c, ":") < 2 || strings.Trim(c, ":.") == "" {
		return 0, 0, false
	}
	if e < len(text) && (isAlnum(text[e]) || text[e] == '_') {
		return 0, 0, false
	}
	if sliceLike(text, s, e) {
		return 0, 0, false
	}
	a, err := netip.ParseAddr(c)
	return s, e, err == nil && a.Is6()
}

func trimIPv6(text string, s, e int) int {
	for e > s && text[e-1] == '.' {
		e--
	}
	if e-s >= 2 && text[e-1] == ':' && text[e-2] != ':' {
		e--
	}
	return e
}

func sliceLike(text string, s, e int) bool {
	if e-s > 5 || s == 0 || e >= len(text) || text[s-1] != '[' || text[e] != ']' {
		return false
	}
	return e+1 >= len(text) || (text[e+1] != ':' && text[e+1] != '/')
}
