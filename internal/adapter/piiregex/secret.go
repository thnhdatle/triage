// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"strings"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const secretConf = 0.95

var secretRe = newRunMatcher(
	`sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{36,}|xox[abprs]-[A-Za-z0-9-]{10,}|`+
		`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{5,}`, 0)

func newSecret() *rule {
	return &rule{
		id:    TypeSecret,
		class: classStructural,
		gate: func(p *profile) bool {
			for _, lit := range [...]string{"sk-", "AKIA", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "xox", "eyJ"} {
				if strings.Contains(p.text, lit) {
					return true
				}
			}
			return false
		},
		scan: scanSecret,
	}
}

func scanSecret(p *profile) []privacy.Finding {
	var out []privacy.Finding
	for _, m := range secretRe.findAll(p.text) {
		s, e := m[0], m[1]
		if s > 0 && (isAlnum(p.text[s-1]) || p.text[s-1] == '_') {
			continue
		}
		v := p.text[s:e]
		if strings.HasPrefix(v, "sk-") && !digitAndLetter(v[3:]) {
			continue // kebab-case words such as "sk-learn-..." are not keys
		}
		if strings.HasPrefix(v, "AKIA") && e < len(p.text) && isAlnum(p.text[e]) {
			continue
		}
		out = append(out, newFinding(p, TypeSecret, s, e, secretConf))
	}
	return out
}

func digitAndLetter(s string) bool {
	var d, l bool
	for i := range len(s) {
		if isDigit(s[i]) {
			d = true
		} else if isAlnum(s[i]) {
			l = true
		}
	}
	return d && l
}
