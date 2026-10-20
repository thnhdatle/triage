// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"strings"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const ibanConf = 0.95

// ibanLengths lists country code plus total IBAN length, four bytes per entry.
const ibanLengths = "AD24AE23AL28AT20AZ28BA20BE16BG22BH22BR29BY28CH21CR22CY28CZ24DE22DK18DO28EE20EG29ES24FI18FO18" +
	"FR27GB22GE22GI23GL18GR27GT28HR21HU28IE22IL23IQ23IS26IT27JO30KW30KZ20LB28LC32LI21LT20LU20LV21MC27MD24" +
	"ME22MK19MR27MT31MU30NL18NO15PK24PL28PS29PT25QA29RO24RS22SA24SC31SE24SI19SK24SM27ST25SV28TL23TN24TR26" +
	"UA29VA22VG24XK20"

var ibanHeadRe = newRunMatcher(`[A-Z]{2}[0-9]{2}`, 0)

func newIBAN() *rule {
	return &rule{
		id:    TypeIBAN,
		class: classChecksum,
		gate:  func(p *profile) bool { return p.digits >= 2 && len(p.text) >= 15 },
		scan:  scanIBAN,
	}
}

func scanIBAN(p *profile) []privacy.Finding {
	var out []privacy.Finding
	for _, m := range ibanHeadRe.findAll(p.text) {
		s := m[0]
		if s > 0 && isAlnum(p.text[s-1]) {
			continue
		}
		length := ibanLength(p.text[s : s+2])
		if length == 0 {
			continue
		}
		e, ok := consumeIBAN(p.text, s, length)
		if !ok || (e < len(p.text) && isAlnum(p.text[e])) {
			continue
		}
		if ibanMod97(p.text[s:e]) {
			out = append(out, newFinding(p, TypeIBAN, s, e, ibanConf))
		}
	}
	return out
}

func ibanLength(country string) int {
	for i := 0; i+4 <= len(ibanLengths); i += 4 {
		if ibanLengths[i:i+2] == country {
			return atoi(ibanLengths[i+2 : i+4])
		}
	}
	return 0
}

func isIBANChar(b byte) bool { return isDigit(b) || (b >= 'A' && b <= 'Z') }

// consumeIBAN reads length IBAN characters from s, allowing single spaces
// between characters, and returns the end offset.
func consumeIBAN(text string, s, length int) (int, bool) {
	count, i := 0, s
	for i < len(text) && count < length {
		switch c := text[i]; {
		case isIBANChar(c):
			count++
			i++
		case c == ' ' && i+1 < len(text) && isIBANChar(text[i+1]):
			i++
		default:
			return 0, false
		}
	}
	return i, count == length
}

// ibanMod97 validates the ISO 13616 check digits of an IBAN (spaces allowed).
func ibanMod97(iban string) bool {
	c := strings.ReplaceAll(iban, " ", "")
	rem := 0
	for i := range len(c) {
		ch := c[(i+4)%len(c)]
		if isDigit(ch) {
			rem = (rem*10 + int(ch-'0')) % 97
			continue
		}
		rem = (rem*100 + int(ch-'A') + 10) % 97
	}
	return rem == 1
}
