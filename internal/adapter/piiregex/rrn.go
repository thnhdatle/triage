// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"time"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const (
	rrnConfChecksum = 0.95 // separator and valid checksum
	rrnConfStruct   = 0.8  // separator, checksum invalid (post-Oct-2020 random tail)
	rrnConfPlain    = 0.9  // 13 plain digits with valid checksum
	rrnConfContext  = 0.75 // 13 plain digits with context word only
)

var rrnRe = newRunMatcher(`\d{6}[- ]\d{7}|\d{13}`, 0)

func newRRN() *rule {
	return &rule{
		id:    TypeKRRRN,
		class: classStructural,
		gate:  func(p *profile) bool { return p.digits >= 13 },
		scan:  scanRRN,
	}
}

func scanRRN(p *profile) []privacy.Finding {
	var out []privacy.Finding
	for _, m := range rrnRe.findAll(p.text) {
		s, e := m[0], m[1]
		d := digitsOf(p.text[s:e])
		if !validRRNDate(d) || !isolated(p.text, s, e) {
			continue
		}
		sum := rrnChecksum(d)
		var conf float64
		switch {
		case e-s == 14 && sum:
			conf = rrnConfChecksum
		case e-s == 14:
			conf = rrnConfStruct
		case sum:
			conf = rrnConfPlain
		case hasContext(rrnContext, p.text, s, e):
			conf = rrnConfContext
		default:
			continue
		}
		out = append(out, newFinding(p, TypeKRRRN, s, e, conf))
	}
	return out
}

// validRRNDate checks YYMMDD and the gender/century digit (1-8) of a 13-digit
// string: 1,2,5,6 are 19xx and 3,4,7,8 are 20xx.
func validRRNDate(d string) bool {
	year := atoi(d[:2])
	switch d[6] {
	case '1', '2', '5', '6':
		year += 1900
	case '3', '4', '7', '8':
		year += 2000
	default:
		return false
	}
	month, day := atoi(d[2:4]), atoi(d[4:6])
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	return day <= time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// rrnChecksum verifies the legacy check digit: weights 2..9,2..5 over the
// first 12 digits, check = (11 - sum mod 11) mod 10.
func rrnChecksum(d string) bool {
	const weights = "234567892345"
	sum := 0
	for i := range 12 {
		sum += int(d[i]-'0') * int(weights[i]-'0')
	}
	return (11-sum%11)%10 == int(d[12]-'0')
}
