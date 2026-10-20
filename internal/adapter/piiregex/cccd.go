// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const (
	cccdConf        = 0.8
	cccdMaxProvince = 96
)

var cccdRe = newRunMatcher(`\d{12}`, 0)

func newCCCD() *rule {
	return &rule{
		id:    TypeVNCCCD,
		class: classContext,
		gate:  func(p *profile) bool { return p.digits >= 12 },
		scan: func(p *profile) []privacy.Finding {
			var out []privacy.Finding
			for _, m := range cccdRe.findAll(p.text) {
				s, e := m[0], m[1]
				if validCCCD(p.text[s:e]) && isolated(p.text, s, e) && hasContext(cccdContext, p.text, s, e) {
					out = append(out, newFinding(p, TypeVNCCCD, s, e, cccdConf))
				}
			}
			return out
		},
	}
}

// validCCCD checks the province code (001-096) and the century/gender digit
// (0-3) of a 12-digit citizen identity number.
func validCCCD(d string) bool {
	province := atoi(d[:3])
	return province >= 1 && province <= cccdMaxProvince && d[3] <= '3'
}
