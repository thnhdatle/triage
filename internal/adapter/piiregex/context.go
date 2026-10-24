// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"regexp"
	"unicode/utf8"
)

const ctxWindow = 32

const (
	ctxHead = `(?i)[^A-Za-z0-9](?:`
	ctxTail = `)[^A-Za-z0-9]`
)

var (
	rrnContext = regexp.MustCompile(ctxHead +
		`주민등록번호|주민번호|주민등록|rrn|resident registration` + ctxTail)
	phoneContext = regexp.MustCompile(ctxHead +
		`phone|tel|telephone|mobile|cell|cellphone|whatsapp|` +
		`전화|전화번호|휴대폰|휴대전화|핸드폰|연락처|` +
		`điện thoại|dien thoai|sđt|sdt|di động|hotline|zalo|số máy` + ctxTail)
	cccdContext = regexp.MustCompile(ctxHead +
		`cccd|căn cước|can cuoc|cmnd|cmt|số định danh|so dinh danh|citizen id|chứng minh` + ctxTail)
)

func hasContext(re *regexp.Regexp, text string, s, e int) bool {
	ws := stepBack(text, s, ctxWindow)
	pre := " "
	if ws > 0 {
		_, n := utf8.DecodeLastRuneInString(text[:ws])
		pre = text[ws-n : ws]
	}
	if re.MatchString(pre + text[ws:s] + " ") {
		return true
	}
	we := stepFwd(text, e, ctxWindow)
	post := " "
	if we < len(text) {
		_, n := utf8.DecodeRuneInString(text[we:])
		post = text[we : we+n]
	}
	return re.MatchString(" " + text[e:we] + post)
}

func stepBack(text string, i, runes int) int {
	for ; runes > 0 && i > 0; runes-- {
		_, n := utf8.DecodeLastRuneInString(text[:i])
		i -= n
	}
	return i
}

func stepFwd(text string, i, runes int) int {
	for ; runes > 0 && i < len(text); runes-- {
		_, n := utf8.DecodeRuneInString(text[i:])
		i += n
	}
	return i
}
