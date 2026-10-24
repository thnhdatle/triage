// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"regexp"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const streetConf = 0.6

var streetRes = [...]*regexp.Regexp{
	regexp.MustCompile(`\b\d{1,5}[A-Za-z]?(?: [A-Z][a-z]+){1,3} ` +
		`(?:Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Lane|Ln|Drive|Dr|Court|Ct|Way|Place|Pl)\b`),
	regexp.MustCompile(`\d+[A-Za-z]?(?:/\d+)* (?:[Đđ]ường|[Pp]hố|[Nn]gõ|[Nn]gách|[Hh]ẻm) ` +
		`\p{Lu}\p{Ll}*(?: \p{Lu}\p{Ll}*){0,3}`),
	regexp.MustCompile(`(?:[가-힣]+(?:도|시) ){0,2}(?:[가-힣]+(?:구|군) )?[가-힣0-9]+(?:대로|로|길) ?\d+(?:-\d+)?`),
}

func newStreet() *rule {
	return &rule{
		id:    TypeStreetAddress,
		class: classHeuristic,
		gate:  func(p *profile) bool { return p.digits >= 1 },
		scan: func(p *profile) []privacy.Finding {
			var cands []candidate
			for _, re := range streetRes {
				for _, m := range re.FindAllStringIndex(p.text, -1) {
					if streetOK(p.text[m[0]:m[1]]) {
						cands = append(cands, candidate{newFinding(p, TypeStreetAddress, m[0], m[1], streetConf), classHeuristic})
					}
				}
			}
			return resolve(cands)
		},
	}
}

func streetOK(v string) bool {
	i := strings.IndexAny(v, "0123456789")
	if i < 0 {
		return false
	}
	name := strings.TrimSpace(v[:i])
	return !strings.HasSuffix(name, "으로")
}
