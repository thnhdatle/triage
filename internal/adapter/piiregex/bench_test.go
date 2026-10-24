// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func prompt2KB() string {
	const para = "Please review the following ticket and summarize the open items. The customer, reachable at " +
		"jo.smith@example.com, reported that order ORD-2024-000123 for $1,299.99 was charged twice on card " +
		"4111 1111 1111 1111 last Monday (2024-03-15 10:30:45). Server 192.168.10.25 returned HTTP 502 for " +
		"build v1.14.2 and the retry loop used x[::2] slices. 고객님의 휴대폰 010-1234-5678 로 연락 부탁드립니다. " +
		"주민등록번호는 900101-1234567 이며 배송지는 서울특별시 강남구 테헤란로 123 입니다. " +
		"Khách hàng để lại SĐT 0912 345 678 và CCCD 079203001234, cảm ơn bạn đã hỗ trợ. "
	var b strings.Builder
	for b.Len() < 2300 {
		b.WriteString(para)
	}
	return b.String()
}

func BenchmarkDetector_Scan2KB(b *testing.B) {
	d, err := New()
	if err != nil {
		b.Fatal(err)
	}
	text := prompt2KB()
	ctx := context.Background()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := d.Detect(ctx, text); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDetector_Detect_BenchPromptHasFindings(t *testing.T) {
	t.Parallel()
	text := prompt2KB()
	assert.GreaterOrEqual(t, len(text), 2000)
	assert.NotEmpty(t, scanAll(t, text))
}

func prose2KB() string {
	const para = "the router reads each request and decides which model should answer it based on the " +
		"estimated difficulty of the prompt while keeping private data away from cloud targets. "
	var b strings.Builder
	b.WriteString("Please summarise the notes. Contact me at user42.bench@example.com about it. ")
	for b.Len() < 1200 {
		b.WriteString(para)
	}
	b.WriteString("Charge card 4031000000000047 for the order. ")
	for b.Len() < 2048 {
		b.WriteString(para)
	}
	return b.String()[:2048]
}

func BenchmarkDetector_ScanProse2KB(b *testing.B) {
	d, err := New()
	if err != nil {
		b.Fatal(err)
	}
	text := prose2KB()
	ctx := context.Background()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := d.Detect(ctx, text); err != nil {
			b.Fatal(err)
		}
	}
}
