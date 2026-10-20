// SPDX-License-Identifier: Apache-2.0

package gen

import (
	"fmt"
	"strings"
)

// negative returns one PII-free prompt made of one to three snippets.
func (g *generator) negative(lang string) Sample {
	n := g.between(1, 3)
	parts := make([]string, 0, n)
	for range n {
		parts = append(parts, g.snippet(lang))
	}
	sep := g.pick(" |\n")
	return Sample{Text: strings.Join(parts, sep), Spans: []Span{}, Lang: lang}
}

func (g *generator) snippet(lang string) string {
	switch g.intn(13) {
	case 0, 1:
		return g.code()
	case 2, 3:
		return g.prose(lang)
	case 4:
		return g.dateTime(lang)
	case 5:
		return g.price(lang)
	case 6:
		return g.version()
	case 7:
		return g.orderID()
	case 8:
		return g.hexID()
	case 9, 10:
		return g.numbers()
	case 11:
		return g.contextOnly()
	}
	return g.hardNegative()
}

func (g *generator) code() string {
	a, b, c := g.between(2, 99), g.between(2, 999), g.between(1, 9)
	switch g.intn(9) {
	case 0:
		return fmt.Sprintf("func add%d(a, b int) int { return a + b + %d }", a, b)
	case 1:
		return fmt.Sprintf("for i := 0; i < %d; i++ { total += i * %d }", b, c)
	case 2:
		return fmt.Sprintf("def f(x):\n    return x[::%d] + x[1::%d]", c, c)
	case 3:
		return fmt.Sprintf("const total = items.reduce((s, i) => s + i.price * %d, 0);", a)
	case 4:
		return fmt.Sprintf("SELECT id, name FROM orders WHERE qty > %d AND total < %d.%02d;", a, b, a)
	case 5:
		return fmt.Sprintf("std::map<std::string, int> m = {{\"a\", %d}}; Foo::Bar::baz(%d);", a, b)
	case 6:
		return fmt.Sprintf(`{"id": %d, "qty": %d, "price": %d.%02d}`, b, a, b, a)
	case 7:
		return fmt.Sprintf("2024-03-15 10:30:45,123 INFO started pid=%d port=%d", b*13, 8000+a)
	}
	return fmt.Sprintf("let arr = [0; %d]; arr[%d::2].iter().sum::<i32>()", b, c)
}

func (g *generator) prose(lang string) string {
	switch lang {
	case "ko":
		return g.pick("회의록을 세 문장으로 요약해 주세요.|파이썬으로 리스트를 정렬하는 함수를 작성해 주세요.|" +
			"오늘 서울 날씨가 어때요?|총 3명이 참석했고 합계는 12,500원입니다.|이 문장을 영어로 번역해 주세요.|" +
			"집으로 3개 보내 주세요.|한국으로 2번 다녀왔어요.|이 보고서는 총 45쪽이고 표가 12개 있습니다.")
	case "vi":
		return g.pick("Hãy tóm tắt cuộc họp hôm qua trong ba câu.|Viết hàm sắp xếp danh sách tên theo thứ tự bảng chữ cái.|" +
			"Tôi có 3 con mèo và 12 quyển sách.|Thời tiết Hà Nội hôm nay thế nào?|Dịch đoạn văn này sang tiếng Anh giúp mình.|" +
			"Báo cáo này có 45 trang và 12 bảng.|Cửa hàng mở cửa từ 8 giờ đến 21 giờ.")
	}
	return g.pick("Can you summarize the meeting notes from last week?|Write a function that sorts a list of names.|" +
		"Explain the difference between TCP and UDP in simple terms.|" +
		"I need a recipe that uses 3 eggs and 200 grams of flour.|" +
		"Translate this paragraph into French and keep the tone formal.|The report has 45 pages and 12 tables.|" +
		"What is the capital of Australia and why was it chosen?|We shipped 1500 units in 2024 and 2300 in 2025.")
}

func (g *generator) dateTime(lang string) string {
	y, m, d := g.between(1990, 2030), g.between(1, 12), g.between(1, 28)
	h, mi, s := g.intn(24), g.intn(60), g.intn(60)
	switch lang {
	case "ko":
		return fmt.Sprintf("%d년 %d월 %d일 오후 %d시 %d분", y, m, d, h%12+1, mi)
	case "vi":
		return fmt.Sprintf("ngày %d tháng %d năm %d, lúc %02d:%02d", d, m, y, h, mi)
	}
	switch g.intn(4) {
	case 0:
		return fmt.Sprintf("%d-%02d-%02d", y, m, d)
	case 1:
		return fmt.Sprintf("%02d/%02d/%d", m, d, y)
	case 2:
		return fmt.Sprintf("%d-%02d-%02dT%02d:%02d:%02dZ", y, m, d, h, mi, s)
	}
	return fmt.Sprintf("meeting at %02d:%02d:%02d", h, mi, s)
}

func (g *generator) price(lang string) string {
	a, b, c := g.between(1, 99), g.intn(1000), g.intn(100)
	switch lang {
	case "ko":
		return fmt.Sprintf("가격은 ₩%d,%03d원입니다.", a, b)
	case "vi":
		return fmt.Sprintf("Giá: %d.%03d.%03dđ", a, b, g.intn(1000))
	}
	if g.chance(50) {
		return fmt.Sprintf("Total: $%d,%03d.%02d", a, b, c)
	}
	return fmt.Sprintf("Price: EUR %d.%02d", a, c)
}

func (g *generator) version() string {
	a, b, c := g.between(0, 20), g.between(0, 30), g.between(0, 99)
	switch g.intn(5) {
	case 0:
		return fmt.Sprintf("v%d.%d.%d", a, b, c)
	case 1:
		return fmt.Sprintf("%d.%d.%d-rc%d", a, b, c, g.between(1, 9))
	case 2:
		return fmt.Sprintf("Python 3.%d.%d", b, c)
	case 3:
		return fmt.Sprintf("v%d.%d.%d.%d", a, b, c, g.between(0, 255))
	}
	return fmt.Sprintf("go1.%d.%d", b, a)
}

func (g *generator) orderID() string {
	switch g.intn(6) {
	case 0:
		return fmt.Sprintf("ORD-2024-%06d", g.intn(1000000))
	case 1:
		return fmt.Sprintf("INV 2024%02d%02d %04d", g.between(1, 12), g.between(1, 28), g.intn(10000))
	case 2:
		return fmt.Sprintf("#A%02d-%04d-%04d", g.intn(100), g.intn(10000), g.intn(10000))
	case 3:
		return fmt.Sprintf("SKU %03d-%03d", g.intn(1000), g.intn(1000))
	case 4:
		return "Order #" + g.digits(g.between(6, 8))
	}
	return fmt.Sprintf("ticket TRG-%04d", g.intn(10000))
}

func (g *generator) hexID() string {
	switch g.intn(3) {
	case 0:
		h := g.charset(32, hexLower)
		return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	case 1:
		return "git commit " + g.charset(40, hexLower)
	}
	return "0x" + g.charset(8, hexLower) + " sha256:" + g.charset(64, hexLower)
}

func (g *generator) numbers() string {
	switch g.intn(9) {
	case 0:
		return "Reference " + g.digits(9)
	case 1:
		return "timestamp 17" + g.digits(11)
	case 2:
		return "epoch 17" + g.digits(8)
	case 3:
		return "Tracking number: " + g.digits(12)
	case 4:
		return fmt.Sprintf("AB-%04d-%04d", g.intn(10000), g.intn(10000))
	case 5:
		return g.pick("3.14159|192.168|1,234,567.89|12:30:45|aa:bb:cc:dd:ee:ff|10:30|1.5:1")
	case 6:
		return g.pick("000-12-3456|666-12-3456|987-65-4321|123-00-4567|123-45-0000")
	case 7:
		return fmt.Sprintf("Mã vận đơn %d", g.between(100000000, 999999999))
	}
	return "Batch " + g.digits(g.between(4, 11))
}

func (g *generator) contextOnly() string {
	return g.pick("Please call me tomorrow, my phone battery is dead.|전화 주세요.|điện thoại của tôi bị hỏng rồi|" +
		"Is the mobile app available on Android?|휴대폰 케이스 추천해 주세요|Số định danh nào dùng cho mẫu đơn này?|" +
		"What does RRN stand for in Korean systems?|주민등록번호는 어디에 쓰이나요?|CCCD là gì?|The hotel phone is on the desk.")
}

// hardNegative returns near-miss PII: right shape, failing validation.
func (g *generator) hardNegative() string {
	switch g.intn(4) {
	case 0:
		return "Card " + bumpLastDigit(g.creditCard())
	case 1:
		return "Account " + bumpLastDigit(g.iban())
	case 2:
		return g.pick("900101-9234567|991341-1234567|900230-1234567|900101-0234567|9001011234567")
	}
	return g.pick("task-force-planning-2024-extra-long-name|risk-assessment-framework-for-teams|" +
		"sk-learn-pipeline-for-text-classification|ghp_ is the prefix GitHub uses|AKIA is a prefix of AWS ids")
}

func bumpLastDigit(s string) string {
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] >= '0' && b[i] <= '9' {
			b[i] = '0' + (b[i]-'0'+1)%10
			break
		}
	}
	return string(b)
}
