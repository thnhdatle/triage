// SPDX-License-Identifier: Apache-2.0

package gen

func (g *generator) frame(lang string) (pre, post string) {
	switch lang {
	case "ko":
		return g.pick("확인 부탁드립니다. |고객 정보: |요청하신 값은 |아래 내용을 봐 주세요. |메모: |"),
			g.pick("|입니다.| 로 처리해 주세요.|이고 확인했습니다.")
	case "vi":
		return g.pick("Xin vui lòng kiểm tra |Thông tin: |Mình gửi |Ghi chú: |"),
			g.pick("| nhé.|. Cảm ơn bạn.| giúp mình nhé.")
	}
	return g.pick("Please check |My details: |Here it is: |The customer entered |Note: |"),
		g.pick("|.|, thanks.|\nLet me know if that works.")
}

func (g *generator) contextPre(typ, lang string) string {
	switch typ + "/" + lang {
	case "phone/ko":
		return g.pick("전화번호: |휴대폰 |연락처: |제 전화는 |전화 ")
	case "phone/vi":
		return g.pick("điện thoại: |SĐT: |số điện thoại của mình là |Hotline |Zalo: ")
	case "phone/en":
		return g.pick("Phone: |Tel: |mobile |My phone number is |Call my mobile: ")
	case "vn_cccd/ko":
		return g.pick("CCCD: |CCCD 번호 ")
	case "vn_cccd/vi":
		return g.pick("CCCD: |số căn cước công dân: |CMND/CCCD |số định danh cá nhân: ")
	case "vn_cccd/en":
		return g.pick("CCCD: |citizen ID (CCCD) |CMND ")
	case "kr_rrn/ko":
		return g.pick("주민등록번호: |주민번호 |주민등록번호는 ")
	case "kr_rrn/vi":
		return "RRN: "
	}
	return g.pick("RRN: |resident registration number: |RRN ")
}

func (g *generator) segment(b *builder, typ, lang string) {
	switch typ {
	case "kr_rrn":
		g.rrnSegment(b, lang)
	case "phone":
		g.phoneSegment(b, lang)
	case "vn_cccd":
		b.lit(g.contextPre(typ, lang))
		b.pii(typ, g.cccd())
		b.lit(g.pick("|.| , thanks"))
	case "street_address":
		g.streetSegment(b, lang)
	case "secret":
		pre, post := g.frame(lang)
		if g.chance(50) {
			pre = g.pick("OPENAI_API_KEY=|export TOKEN=|Authorization: Bearer ")
		}
		b.lit(pre)
		b.pii(typ, g.secret())
		b.lit(post)
	default:
		pre, post := g.frame(lang)
		b.lit(pre)
		b.pii(typ, g.value(typ))
		b.lit(post)
	}
}

func (g *generator) value(typ string) string {
	switch typ {
	case "credit_card":
		return g.creditCard()
	case "iban":
		return g.iban()
	case "email":
		return g.email()
	case "ip":
		return g.ip()
	}
	return g.ssn()
}

func (g *generator) rrnSegment(b *builder, lang string) {
	r := g.intn(100)
	switch {
	case r < 65:
		pre, post := g.frame(lang)
		d := g.rrn(g.chance(40))
		sep := "-"
		if r >= 55 {
			sep = " "
		}
		b.lit(pre)
		b.pii("kr_rrn", d[:6]+sep+d[6:])
		b.lit(post)
	default:
		ctx := g.chance(85)
		d := g.rrn(!ctx || g.chance(40))
		for luhnCheck(d[:12]) == int(d[12]-'0') {
			d = g.rrn(!ctx || g.chance(40))
		}
		if ctx {
			b.lit(g.contextPre("kr_rrn", lang))
		} else {
			pre, _ := g.frame(lang)
			b.lit(pre)
		}
		b.pii("kr_rrn", d)
		b.lit(g.pick("|.|입니다."))
	}
}

func (g *generator) phoneSegment(b *builder, lang string) {
	if g.chance(40) {
		pre, post := g.frame(lang)
		if g.chance(50) {
			pre = g.contextPre("phone", lang)
		}
		b.lit(pre)
		b.pii("phone", g.e164())
		b.lit(post)
		return
	}
	b.lit(g.contextPre("phone", lang))
	switch lang {
	case "ko":
		b.pii("phone", g.krPhone())
	case "vi":
		b.pii("phone", g.vnPhone())
	default:
		b.pii("phone", g.usPhone())
	}
	b.lit(g.pick("|.|, thanks"))
}

func (g *generator) streetSegment(b *builder, lang string) {
	switch lang {
	case "ko":
		b.lit(g.pick("주소: |배송지는 |위치가 |본사는 "))
		b.pii("street_address", g.street(lang))
		b.lit(g.pick("|입니다.| 로 보내주세요."))
	case "vi":
		b.lit(g.pick("Địa chỉ: |Nhà mình ở |Giao hàng tới "))
		b.pii("street_address", g.street(lang))
		b.lit(g.pick("|.|, Hà Nội|, quận 1."))
	default:
		b.lit(g.pick("Ship it to |Our office is at |Address: |Deliver to "))
		b.pii("street_address", g.street(lang))
		b.lit(g.pick("|.|, Springfield|, thanks."))
	}
}
