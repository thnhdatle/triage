// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type valueCase struct {
	name string
	text string
	want []string
}

func runValueCases(t *testing.T, id string, cases []valueCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := values(scanOne(t, id, tc.text))
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCreditCard_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeCreditCard, []valueCase{
		{"visa plain", "card 4111111111111111 ok", []string{"4111111111111111"}},
		{"visa spaced", "card 4111 1111 1111 1111.", []string{"4111 1111 1111 1111"}},
		{"visa dashed", "card 4111-1111-1111-1111", []string{"4111-1111-1111-1111"}},
		{"amex 4-6-5", "amex 3782 822463 10005", []string{"3782 822463 10005"}},
		{"mastercard", "5555555555554444", []string{"5555555555554444"}},
		{"visa 13 digits", "4222222222222", []string{"4222222222222"}},
		{"luhn fail", "4111 1111 1111 1112", nil},
		{"implausible issuer", "1234567812345670", nil},
		{"too short", "411111111111", nil},
		{"too long run", "41111111111111111111", nil},
		{"glued to more digits", "94111111111111111", nil},
		{"continues a longer group sequence", "ref 12 4111 1111 1111 1111", nil},
		{"inside an iban-like sequence", "FR30 6774 4759 3051 9606 6912 66", nil},
		{"rrn layout is not a card", "900101-1234567", nil},
		{"korean text", "카드번호 4111 1111 1111 1111 입니다", []string{"4111 1111 1111 1111"}},
	})
}

func TestIBAN_Detect_Cases(t *testing.T) {
	t.Parallel()
	runValueCases(t, TypeIBAN, []valueCase{
		{"germany plain", "DE89370400440532013000", []string{"DE89370400440532013000"}},
		{"germany spaced", "iban DE89 3704 0044 0532 0130 00.", []string{"DE89 3704 0044 0532 0130 00"}},
		{"uk with letters", "GB82 WEST 1234 5698 7654 32", []string{"GB82 WEST 1234 5698 7654 32"}},
		{"wrong check digits", "DE88 3704 0044 0532 0130 00", nil},
		{"wrong length", "DE89 3704 0044 0532 0130", nil},
		{"unknown country", "XX89 3704 0044 0532 0130 00", nil},
		{"glued prefix", "ZZDE89370400440532013000", nil},
		{"trailing alnum", "DE89370400440532013000X", nil},
		{"space before digit run ends", "DE89 3704 0044 0532 0130 0", nil},
		{"double space", "DE89  3704 0044 0532 0130 00", nil},
		{"lowercase not matched", "de89370400440532013000", nil},
	})
}

func TestCreditCard_Detect_Confidence(t *testing.T) {
	t.Parallel()
	fs := scanOne(t, TypeCreditCard, "4111111111111111")
	assert.Len(t, fs, 1)
	assert.InDelta(t, 0.95, fs[0].Confidence, 1e-9)
	assert.Equal(t, TypeCreditCard, fs[0].Type)
}
