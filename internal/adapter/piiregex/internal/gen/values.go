// SPDX-License-Identifier: Apache-2.0

package gen

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	// AllTypes lists the labelled PII types, "|"-separated.
	AllTypes = "credit_card|iban|email|ip|us_ssn|secret|kr_rrn|phone|vn_cccd|street_address"
	alnum    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	upperNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	urlSafe  = alnum + "-_"
	hexLower = "0123456789abcdef"
)

func langOf(i int) string { return [...]string{"en", "ko", "vi"}[i%3] }

func luhnCheck(payload string) int {
	sum := 0
	for i, pos := len(payload)-1, 0; i >= 0; i, pos = i-1, pos+1 {
		v := int(payload[i] - '0')
		if pos%2 == 0 {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return (10 - sum%10) % 10
}

func group(d string, sizes []int, sep string) string {
	parts := make([]string, 0, len(sizes))
	for _, n := range sizes {
		parts = append(parts, d[:n])
		d = d[n:]
	}
	return strings.Join(parts, sep)
}

func (g *generator) creditCard() string {
	type scheme struct {
		prefix string
		n      int
	}
	schemes := [...]scheme{
		{"4", 16}, {"51", 16}, {"55", 16}, {"34", 15}, {"37", 15}, {"6011", 16}, {"3530", 16}, {"36", 14}, {"62", 16},
	}
	s := schemes[g.intn(len(schemes))]
	payload := s.prefix + g.digits(s.n-1-len(s.prefix))
	d := payload + strconv.Itoa(luhnCheck(payload))
	var sizes []int
	switch s.n {
	case 15:
		sizes = []int{4, 6, 5}
	case 14:
		sizes = []int{4, 6, 4}
	default:
		sizes = []int{4, 4, 4, 4}
	}
	switch g.intn(3) {
	case 0:
		return d
	case 1:
		return group(d, sizes, " ")
	}
	return group(d, sizes, "-")
}

func (g *generator) iban() string {
	type country struct {
		code string
		bban func() string
	}
	countries := [...]country{
		{"DE", func() string { return g.digits(18) }},
		{"GB", func() string { return g.charset(4, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") + g.digits(14) }},
		{"FR", func() string { return g.digits(23) }},
		{"ES", func() string { return g.digits(20) }},
		{"NL", func() string { return g.charset(4, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") + g.digits(10) }},
		{"IT", func() string { return g.charset(1, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") + g.digits(22) }},
		{"BE", func() string { return g.digits(12) }},
		{"AT", func() string { return g.digits(16) }},
		{"CH", func() string { return g.digits(5) + g.charset(12, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ") }},
		{"PL", func() string { return g.digits(24) }},
	}
	c := countries[g.intn(len(countries))]
	bban := c.bban()
	rem := 0
	for _, ch := range []byte(bban + c.code + "00") {
		if ch >= '0' && ch <= '9' {
			rem = (rem*10 + int(ch-'0')) % 97
		} else {
			rem = (rem*100 + int(ch-'A') + 10) % 97
		}
	}
	iban := fmt.Sprintf("%s%02d%s", c.code, 98-rem, bban)
	if g.chance(50) {
		return iban
	}
	var parts []string
	for len(iban) > 4 {
		parts = append(parts, iban[:4])
		iban = iban[4:]
	}
	return strings.Join(append(parts, iban), " ")
}

func (g *generator) email() string {
	user := g.pick("alice|bob|nguyen.van.a|kim.minsu|park_jihoon|tran.thi.b|john.smith|support|dev+test|lee.soyeon")
	if g.chance(50) {
		user += g.digits(g.between(1, 3))
	}
	return user + "@" + g.pick("example.com|mail.example.org|corp.co.kr|gmail.com|naver.com|company.vn|vnu.edu.vn|sub.io")
}

func (g *generator) hexGroup() string { return fmt.Sprintf("%x", g.intn(0x10000)) }

func (g *generator) ip() string {
	if g.chance(55) {
		return fmt.Sprintf("%d.%d.%d.%d", g.between(1, 223), g.intn(256), g.intn(256), g.between(1, 254))
	}
	switch g.intn(5) {
	case 0:
		parts := make([]string, 8)
		for i := range parts {
			parts[i] = g.hexGroup()
		}
		return strings.Join(parts, ":")
	case 1:
		return "2001:db8::" + g.hexGroup()
	case 2:
		return "fe80::" + g.hexGroup() + ":" + g.hexGroup() + ":" + g.hexGroup() + ":" + g.hexGroup()
	case 3:
		return "::1"
	}
	head := g.hexGroup() + ":" + g.hexGroup()
	return head + "::" + g.hexGroup() + ":" + g.hexGroup()
}

func (g *generator) ssn() string {
	area := g.between(1, 899)
	for area == 666 {
		area = g.between(1, 899)
	}
	sep := g.pick("-|-|-| ")
	return fmt.Sprintf("%03d%s%02d%s%04d", area, sep, g.between(1, 99), sep, g.between(1, 9999))
}

func (g *generator) secret() string {
	switch g.intn(5) {
	case 0:
		body := g.charset(g.between(40, 48), alnum)
		return "sk-" + g.pick("|proj-|ant-api03-") + body[:5] + g.digits(1) + body[5:]
	case 1:
		return "AKIA" + g.charset(16, upperNum)
	case 2:
		return "ghp_" + g.charset(36, alnum)
	case 3:
		return "xoxb-" + g.digits(12) + "-" + g.digits(12) + "-" + g.charset(24, alnum)
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := enc.EncodeToString([]byte(fmt.Sprintf(`{"sub":"%s","iat":%s}`, g.digits(10), g.digits(10))))
	return header + "." + payload + "." + g.charset(43, urlSafe)
}

func rrnCheck(d12 string) int {
	const weights = "234567892345"
	sum := 0
	for i := range 12 {
		sum += int(d12[i]-'0') * int(weights[i]-'0')
	}
	return (11 - sum%11) % 10
}

// rrn returns 13 digits for a valid birth date and gender digit.
func (g *generator) rrn(validChecksum bool) string {
	year := g.between(1950, 2020)
	if g.chance(30) {
		year = g.between(2021, 2025) // issued after October 2020: random tail
	}
	genders := "12|12|12|56"
	if year >= 2000 {
		genders = "34|34|34|78"
	}
	gender := g.pick(genders)[g.intn(2)]
	d12 := fmt.Sprintf("%02d%02d%02d%c%s", year%100, g.between(1, 12), g.between(1, 28), gender, g.digits(5))
	check := rrnCheck(d12)
	if !validChecksum {
		check = (check + g.between(1, 9)) % 10
	}
	return d12 + strconv.Itoa(check)
}

func (g *generator) cccd() string {
	return fmt.Sprintf("%03d%d%02d%s", g.between(1, 96), g.intn(4), g.intn(100), g.digits(6))
}

func (g *generator) usPhone() string {
	area := fmt.Sprintf("%d%s", g.between(2, 9), g.digits(2))
	exch := fmt.Sprintf("%d%s", g.between(2, 9), g.digits(2))
	line := g.digits(4)
	switch g.intn(4) {
	case 0:
		return "(" + area + ") " + exch + "-" + line
	case 1:
		return area + "-" + exch + "-" + line
	case 2:
		return area + "." + exch + "." + line
	}
	return area + " " + exch + " " + line
}

func (g *generator) krPhone() string {
	sep := g.pick("-|-| |.|")
	if g.chance(75) {
		return "010" + sep + g.digits(4) + sep + g.digits(4)
	}
	area := g.pick("02|031|032|051|053|042|062")
	mid := g.digits(g.between(3, 4))
	return area + sep + mid + sep + g.digits(4)
}

func (g *generator) vnPhone() string {
	prefix := g.pick("090|091|093|094|096|097|098|032|033|035|037|038|039|070|076|077|078|079|081|083|085|086|088|089|" +
		"056|058|059")
	first := prefix + g.digits(1)
	switch g.intn(4) {
	case 0:
		return first + g.digits(6)
	case 1:
		return first + "." + g.digits(3) + "." + g.digits(3)
	case 2:
		return first + "-" + g.digits(3) + "-" + g.digits(3)
	}
	return first + " " + g.digits(3) + " " + g.digits(3)
}

func (g *generator) e164() string {
	switch g.intn(8) {
	case 0:
		d := g.pick("9|3|7|8") + g.digits(8)
		return "+84 " + d[:3] + " " + d[3:6] + " " + d[6:]
	case 1:
		return "+84" + g.pick("9|3|7|8") + g.digits(8)
	case 2:
		return "+82 10-" + g.digits(4) + "-" + g.digits(4)
	case 3:
		return "+82 10 " + g.digits(4) + " " + g.digits(4)
	case 4:
		return fmt.Sprintf("+1 %d%s %d%s %s", g.between(2, 9), g.digits(2), g.between(2, 9), g.digits(2), g.digits(4))
	case 5:
		return fmt.Sprintf("+1 (%d%s) %d%s-%s", g.between(2, 9), g.digits(2), g.between(2, 9), g.digits(2), g.digits(4))
	case 6:
		return "+44 20 " + g.digits(4) + " " + g.digits(4)
	}
	return "+49 30 " + g.digits(7)
}

func (g *generator) street(lang string) string {
	switch lang {
	case "ko":
		return g.pick("서울특별시 강남구 테헤란로 123|세종대로 110|부산광역시 해운대구 해운대해변로 264|" +
			"경기도 성남시 판교로 235|서울시 마포구 월드컵북로 396|대구광역시 수성구 달구벌대로 2500|" +
			"제주특별자치도 제주시 첨단로 242|가로수길 10|봉은사로 524")
	case "vi":
		num := strconv.Itoa(g.between(1, 400))
		if g.chance(25) {
			num += "/" + strconv.Itoa(g.between(1, 60))
		}
		return num + " " + g.pick("đường|phố|ngõ|hẻm|Đường") + " " +
			g.pick("Lê Lợi|Nguyễn Huệ|Hàng Bông|Trần Hưng Đạo|Láng|Cầu Giấy|Hai Bà Trưng")
	}
	names := g.pick("Main|Oak|Maple|Pennsylvania|Baker|Elm|Washington|Lincoln|Market|Fifth|Park Lane")
	return fmt.Sprintf("%d %s %s", g.between(1, 9999), names,
		g.pick("Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Drive|Dr|Court|Way"))
}
