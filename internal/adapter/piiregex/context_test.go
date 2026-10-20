// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPhone_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypePhone, []valueCase{
		{"e164 without context", "reach +84 912 345 678 today", []string{"+84 912 345 678"}},
		{"e164 plain", "+84912345678", []string{"+84912345678"}},
		{"e164 us parens", "+1 (415) 555-2671.", []string{"+1 (415) 555-2671"}},
		{"e164 korea", "+82 10-1234-5678", []string{"+82 10-1234-5678"}},
		{"e164 too many digits trimmed", "+84 912 345 678 901 234 567", []string{"+84 912 345 678 901"}},
		{"e164 no separator too long", "+123456789012345678", nil},
		{"e164 too short", "+1234567", nil},
		{"e164 glued to word", "x+84912345678", nil},
		{"e164 zero first", "+0123456789", nil},
		{"kr mobile with context", "휴대폰 010-1234-5678", []string{"010-1234-5678"}},
		{"kr mobile without context", "010-1234-5678", nil},
		{"kr plain digits", "Tel: 01012345678", []string{"01012345678"}},
		{"kr landline", "전화 02-123-4567", []string{"02-123-4567"}},
		{"vn mobile spaced", "SĐT: 0912 345 678", []string{"0912 345 678"}},
		{"vn landline", "điện thoại 024 3825 1234", []string{"024 3825 1234"}},
		{"vn twelve digit cccd is not a landline", "phone 025360155164", nil},
		{"vn without context", "0912 345 678", nil},
		{"us dashed", "Phone: 415-555-2671.", []string{"415-555-2671"}},
		{"us parens", "tel (415) 555-2671", []string{"(415) 555-2671"}},
		{"us leading 1", "mobile 1-415-555-2671", []string{"1-415-555-2671"}},
		{"us area starts with 1", "phone 115-555-2671", nil},
		{"us exchange starts with 1", "phone 415-155-2671", nil},
		{"us without context", "415-555-2671", nil},
		{"context inside other word", "hotel 415-555-2671", nil},
		{"glued to longer number", "phone 4155552671234", nil},
	})
}

func TestPhone_Detect_ContextWindow(t *testing.T) {
	t.Parallel()
	const num = "010-1234-5678"
	cases := []struct {
		name   string
		text   string
		wantOK bool
	}{
		{"before 32", "tel" + strings.Repeat(" ", 29) + num, true},
		{"before 33", "tel" + strings.Repeat(" ", 30) + num, false},
		{"after 32", num + strings.Repeat(" ", 29) + "tel", true},
		{"after 33", num + strings.Repeat(" ", 30) + "tel", false},
		{"case insensitive", "PHONE: " + num, true},
		{"vietnamese uppercase", "SĐT " + num, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := scanOne(t, TypePhone, tc.text)
			if tc.wantOK {
				require.Len(t, fs, 1)
				assert.Equal(t, num, fs[0].Value)
				return
			}
			assert.Empty(t, fs)
		})
	}
}

func TestCCCD_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeVNCCCD, []valueCase{
		{"with context", "CCCD: 012345678901", []string{"012345678901"}},
		{"vietnamese context", "số căn cước công dân của tôi là 079203001234", []string{"079203001234"}},
		{"cmnd context after", "079203001234 (CMND)", []string{"079203001234"}},
		{"so dinh danh", "Số định danh: 001099012345", []string{"001099012345"}},
		{"no context", "012345678901", nil},
		{"province 000", "CCCD 000345678901", nil},
		{"province 097", "CCCD 097345678901", nil},
		{"province 096", "CCCD 096345678901", []string{"096345678901"}},
		{"century digit 4", "CCCD 012445678901", nil},
		{"11 digits", "CCCD 01234567890", nil},
		{"13 digits", "CCCD 0123456789012", nil},
	})
}

func TestStreetAddress_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeStreetAddress, []valueCase{
		{"english", "Ship it to 221 Baker Street.", []string{"221 Baker Street"}},
		{"english abbreviation", "at 1600 Pennsylvania Ave, DC", []string{"1600 Pennsylvania Ave"}},
		{"english two word name", "12 Park Lane Road", []string{"12 Park Lane Road"}},
		{"korean full", "주소: 서울특별시 강남구 테헤란로 123 입니다", []string{"서울특별시 강남구 테헤란로 123"}},
		{"korean province", "경기도 성남시 판교로 235", []string{"경기도 성남시 판교로 235"}},
		{"korean short", "세종대로 110", []string{"세종대로 110"}},
		{"korean particle", "집으로 3개 보내 주세요", nil},
		{"vietnamese", "Địa chỉ: 12/5 đường Lê Lợi, Hà Nội", []string{"12/5 đường Lê Lợi"}},
		{"vietnamese stops at lowercase", "45 phố Hàng Bông quận 1", []string{"45 phố Hàng Bông"}},
		{"no number", "Baker Street", nil},
		{"plain prose", "I live on a street with 3 houses", nil},
	})
}

func TestStreetAddress_New_DisabledByDefault(t *testing.T) {
	t.Parallel()
	const text = "Ship it to 221 Baker Street."
	d, err := New()
	require.NoError(t, err)
	fs, err := d.Detect(t.Context(), text)
	require.NoError(t, err)
	assert.Empty(t, fs)
	assert.Len(t, d.Detectors(), 9)

	on, err := New(WithStreetAddress(true))
	require.NoError(t, err)
	fs, err = on.Detect(t.Context(), text)
	require.NoError(t, err)
	require.Len(t, fs, 1)
	assert.Equal(t, TypeStreetAddress, fs[0].Type)
	assert.Equal(t, TypeStreetAddress, fs[0].DetectorID)
	assert.Len(t, on.Detectors(), 10)

	off, err := New(WithStreetAddress(true), WithStreetAddress(false))
	require.NoError(t, err)
	assert.Len(t, off.Detectors(), 9)
}
