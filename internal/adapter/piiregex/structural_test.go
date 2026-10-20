// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmail_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeEmail, []valueCase{
		{"simple", "mail jo@example.com now", []string{"jo@example.com"}},
		{"plus and dots", "a.b+tag@mail.example.co.kr.", []string{"a.b+tag@mail.example.co.kr"}},
		{"after hangul", "이메일:kim@naver.com입니다", []string{"kim@naver.com"}},
		{"no tld", "jo@localhost", nil},
		{"no local part", "@example.com", nil},
		{"at sign only", "a @ b", nil},
	})
}

func TestIP_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeIP, []valueCase{
		{"v4", "host 192.168.1.1.", []string{"192.168.1.1"}},
		{"v4 port", "10.0.0.1:8080", []string{"10.0.0.1"}},
		{"octet too big", "256.1.1.1", nil},
		{"leading zero", "01.2.3.4", nil},
		{"version prefix", "v1.2.3.4", nil},
		{"five parts", "1.2.3.4.5", nil},
		{"three parts", "1.2.3", nil},
		{"v6 compressed", "addr 2001:db8::1 up", []string{"2001:db8::1"}},
		{"v6 loopback", "bind ::1.", []string{"::1"}},
		{"v6 full", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", []string{"2001:0db8:85a3:0000:0000:8a2e:0370:7334"}},
		{"v6 mapped v4", "::ffff:192.0.2.1", []string{"::ffff:192.0.2.1"}},
		{"v6 trailing colon", "fe80::1:", []string{"fe80::1"}},
		{"bracketed v6 with port", "http://[::1]:8080/", []string{"::1"}},
		{"time of day", "at 12:30:45", nil},
		{"mac address", "aa:bb:cc:dd:ee:ff", nil},
		{"python slice", "x[::2]", nil},
		{"cpp scope", "std::vector<int>", nil},
		{"only colons", "::", nil},
		{"glued to word", "id_::1", nil},
		{"glued after", "::1x", nil},
	})
}

func TestUSSSN_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeUSSSN, []valueCase{
		{"dashed", "ssn 123-45-6789.", []string{"123-45-6789"}},
		{"spaced", "ssn 123 45 6789", []string{"123 45 6789"}},
		{"area 000", "000-45-6789", nil},
		{"area 666", "666-45-6789", nil},
		{"area 900", "900-45-6789", nil},
		{"area 987", "987-65-4321", nil},
		{"area 899", "899-45-6789", []string{"899-45-6789"}},
		{"group 00", "123-00-6789", nil},
		{"serial 0000", "123-45-0000", nil},
		{"mixed separators", "123-45 6789", nil},
		{"longer dashed number", "2024-123-45-6789", nil},
		{"undashed", "123456789", nil},
	})
}

func TestSecret_Detect_Cases(t *testing.T) {
	t.Parallel()
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r"
	runValueCases(t, TypeSecret, []valueCase{
		{"openai", "key sk-proj-abc123def456ghi789jkl012mno345", []string{"sk-proj-abc123def456ghi789jkl012mno345"}},
		{"aws", "AKIAIOSFODNN7EXAMPLE", []string{"AKIAIOSFODNN7EXAMPLE"}},
		{"aws too long", "AKIAIOSFODNN7EXAMPLEXX", nil},
		{"github", "ghp_" + strings.Repeat("a1", 18), []string{"ghp_" + strings.Repeat("a1", 18)}},
		{"slack", "xoxb-123456789012-123456789012-abcdefABCDEF", []string{"xoxb-123456789012-123456789012-abcdefABCDEF"}},
		{"jwt", "Bearer " + jwt, []string{jwt}},
		{"kebab words", "task-force-planning-2024-extra-long-name", nil},
		{"sk without digit", "sk-learn-pipeline-for-text-classification", nil},
		{"glued prefix", "xsk-abc123def456ghi789jkl012mno", nil},
		{"short key", "sk-abc123", nil},
	})
}

func TestKRRRN_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeKRRRN, []valueCase{
		{"dashed valid checksum", "900101-1234568", []string{"900101-1234568"}},
		{"dashed invalid checksum still found", "900101-1234567", []string{"900101-1234567"}},
		{"spaced", "900101 1234567", []string{"900101 1234567"}},
		{"post 2020 random tail", "생년월일 210315-3456789", []string{"210315-3456789"}},
		{"foreign gender digit 5", "900101-5234567", []string{"900101-5234567"}},
		{"gender digit 8", "050101-8234567", []string{"050101-8234567"}},
		{"gender digit 9", "900101-9234567", nil},
		{"gender digit 0", "900101-0234567", nil},
		{"month 13", "901301-1234567", nil},
		{"day 32", "900132-1234567", nil},
		{"feb 30", "900230-1234567", nil},
		{"leap day 2000", "000229-3234567", []string{"000229-3234567"}},
		{"non leap day 1900s", "910229-1234567", nil},
		{"undashed no context invalid checksum", "9001011234567", nil},
		{"undashed valid checksum no context", "9001011234568", []string{"9001011234568"}},
		{"undashed context", "주민등록번호: 9001011234567", []string{"9001011234567"}},
		{"undashed english context", "RRN 9001011234567", []string{"9001011234567"}},
		{"undashed resident registration", "resident registration 9001011234567", []string{"9001011234567"}},
		{"glued digits", "19001011234567", nil},
	})
}

func TestKRRRN_Detect_ContextWindow(t *testing.T) {
	t.Parallel()
	const rrn = "9001011234567"
	cases := []struct {
		name   string
		text   string
		wantOK bool
	}{
		{"before 32 chars", "RRN" + strings.Repeat(" ", 29) + rrn, true},
		{"before 33 chars", "RRN" + strings.Repeat(" ", 30) + rrn, false},
		{"after 32 chars", rrn + strings.Repeat(" ", 29) + "RRN", true},
		{"after 33 chars", rrn + strings.Repeat(" ", 30) + "RRN", false},
		{"hangul runes not bytes", "주민번호" + strings.Repeat("가", 28) + rrn, true},
		{"hangul 33 runes", "주민번호" + strings.Repeat("가", 29) + rrn, false},
		{"word cut by window", "xRRN" + strings.Repeat(" ", 29) + rrn, false},
		{"embedded word", "xRRNx " + rrn, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := scanOne(t, TypeKRRRN, tc.text)
			if tc.wantOK {
				require.Len(t, fs, 1)
				assert.Equal(t, rrn, fs[0].Value)
				return
			}
			assert.Empty(t, fs)
		})
	}
}

func TestKRRRN_Detect_Confidence(t *testing.T) {
	t.Parallel()
	conf := func(text string) float64 {
		fs := scanOne(t, TypeKRRRN, text)
		require.Len(t, fs, 1)
		return fs[0].Confidence
	}
	assert.Greater(t, conf("900101-1234568"), conf("900101-1234567"))
	assert.Greater(t, conf("9001011234568"), conf("RRN 9001011234567"))
}

func TestProfile_Gate_SkipsImpossibleRules(t *testing.T) {
	t.Parallel()
	d, err := New(WithStreetAddress(true))
	require.NoError(t, err)
	p := newProfile("just plain words without digits")
	for _, r := range d.rules {
		assert.Falsef(t, r.gate(&p), "rule %s should be gated off", r.id)
	}
	p = newProfile("a@b.co 1.2.3.4 fe80::1")
	assert.True(t, p.at && p.colon && p.dot)
	assert.Equal(t, 7, p.digits)
}
