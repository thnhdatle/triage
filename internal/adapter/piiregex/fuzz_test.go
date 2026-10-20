// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"context"
	"testing"
	"unicode/utf8"
)

func FuzzDetector(f *testing.F) {
	for _, seed := range []string{
		"", "jo@example.com", "4111 1111 1111 1111", "DE89 3704 0044 0532 0130 00",
		"RRN 9001011234567", "주민등록번호: 900101-1234567", "SĐT: 0912 345 678", "+84 912 345 678",
		"2001:db8::1 and ::ffff:1.2.3.4", "123-45-6789", "sk-proj-abc123def456ghi789jkl012mno345",
		"CCCD: 012345678901", "221 Baker Street", "서울특별시 강남구 테헤란로 123", "\xff\xfe invalid utf8 \x00",
	} {
		f.Add(seed)
	}
	d, err := New(WithStreetAddress(true))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, text string) {
		fs, err := d.Detect(context.Background(), text)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		prevEnd := 0
		for _, fd := range fs {
			if fd.Start < 0 || fd.Start >= fd.End || fd.End > len(text) {
				t.Fatalf("bad range [%d,%d) for text of %d bytes", fd.Start, fd.End, len(text))
			}
			if fd.Value != text[fd.Start:fd.End] {
				t.Fatalf("value %q != text[%d:%d]", fd.Value, fd.Start, fd.End)
			}
			if fd.Start < prevEnd {
				t.Fatalf("findings overlap or are unsorted at %d", fd.Start)
			}
			prevEnd = fd.End
			if utf8.ValidString(text) && !utf8.ValidString(fd.Value) {
				t.Fatalf("finding %q splits a rune", fd.Value)
			}
		}
	})
}
