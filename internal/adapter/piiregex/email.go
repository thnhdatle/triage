// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const emailConf = 0.9

var emailRe = newRunMatcher(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`, '@')

func newEmail() *rule {
	return &rule{
		id:    TypeEmail,
		class: classStructural,
		gate:  func(p *profile) bool { return p.at && p.dot },
		scan: func(p *profile) []privacy.Finding {
			ms := emailRe.findAll(p.text)
			out := make([]privacy.Finding, 0, len(ms))
			for _, m := range ms {
				out = append(out, newFinding(p, TypeEmail, m[0], m[1], emailConf))
			}
			return out
		},
	}
}
