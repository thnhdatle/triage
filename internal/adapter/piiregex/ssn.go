// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const ssnConf = 0.85

var ssnRe = newRunMatcher(`\d{3}[- ]\d{2}[- ]\d{4}`, 0)

func newSSN() *rule {
	return &rule{
		id:    TypeUSSSN,
		class: classStructural,
		gate:  func(p *profile) bool { return p.digits >= 9 },
		scan: func(p *profile) []privacy.Finding {
			var out []privacy.Finding
			for _, m := range ssnRe.findAll(p.text) {
				s, e := m[0], m[1]
				if p.text[s+3] == p.text[s+6] && validSSN(p.text[s:e]) && isolated(p.text, s, e) {
					out = append(out, newFinding(p, TypeUSSSN, s, e, ssnConf))
				}
			}
			return out
		},
	}
}

// validSSN applies the SSA assignment rules: area not 000, 666 or 900-999,
// group not 00, serial not 0000.
func validSSN(v string) bool {
	area, group, serial := atoi(v[:3]), atoi(v[4:6]), atoi(v[7:])
	return area != 0 && area != 666 && area < 900 && group != 0 && serial != 0
}
