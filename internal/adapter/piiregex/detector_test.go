// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

func TestDetector_ID_IsPiiregex(t *testing.T) {
	t.Parallel()
	d, err := New()
	require.NoError(t, err)
	assert.Equal(t, "piiregex", d.ID())
	var _ privacy.Detector = d
	ids := make([]string, 0, 9)
	for _, sub := range d.Detectors() {
		ids = append(ids, sub.ID())
	}
	assert.Equal(t, []string{
		"credit_card", "iban", "email", "ip", "us_ssn", "secret", "kr_rrn", "phone", "vn_cccd",
	}, ids)
}

func TestDetector_Detect_AllTypesWithDetectorIDs(t *testing.T) {
	t.Parallel()
	text := strings.Join([]string{
		"card 4111 1111 1111 1111",
		"iban DE89 3704 0044 0532 0130 00",
		"mail jo@example.com",
		"ip 192.168.1.1",
		"ssn 123-45-6789",
		"key AKIAIOSFODNN7EXAMPLE",
		"rrn 900101-1234567",
		"phone +84 912 345 678",
		"CCCD: 012345678901",
		"addr 221 Baker Street",
	}, "\n")
	fs := scanAll(t, text)
	got := map[string]string{}
	for _, f := range fs {
		assert.Equal(t, f.Type, f.DetectorID)
		assert.Equal(t, text[f.Start:f.End], f.Value)
		assert.Greater(t, f.Confidence, 0.0)
		got[f.Type] = f.Value
	}
	assert.Equal(t, map[string]string{
		"credit_card":    "4111 1111 1111 1111",
		"iban":           "DE89 3704 0044 0532 0130 00",
		"email":          "jo@example.com",
		"ip":             "192.168.1.1",
		"us_ssn":         "123-45-6789",
		"secret":         "AKIAIOSFODNN7EXAMPLE",
		"kr_rrn":         "900101-1234567",
		"phone":          "+84 912 345 678",
		"vn_cccd":        "012345678901",
		"street_address": "221 Baker Street",
	}, got)
	for i := 1; i < len(fs); i++ {
		assert.LessOrEqual(t, fs[i-1].End, fs[i].Start, "findings must be sorted and disjoint")
	}
}

func TestDetector_Detect_ByteOffsetsOnUTF8(t *testing.T) {
	t.Parallel()
	text := "안녕하세요 홍길동입니다. 메일은 jo@example.com 이고 전화는 010-1234-5678 입니다. Xin chào, SĐT 0912 345 678."
	fs := scanAll(t, text)
	require.Len(t, fs, 3)
	for _, f := range fs {
		assert.Equal(t, strings.Index(text, f.Value), f.Start)
		assert.Equal(t, f.Start+len(f.Value), f.End)
		assert.Equal(t, text[f.Start:f.End], f.Value)
	}
	assert.Equal(t, []string{"jo@example.com", "010-1234-5678", "0912 345 678"}, values(fs))
}

func TestDetector_Detect_ChecksumBeatsOtherClasses(t *testing.T) {
	t.Parallel()
	var both string
	for i := range 100000 {
		d12 := fmt.Sprintf("4001011%05d", i)
		for c := range 10 {
			d := d12 + strconv.Itoa(c)
			if luhnValid(d) && validRRNDate(d) {
				both = d
			}
		}
		if both != "" {
			break
		}
	}
	require.NotEmpty(t, both)
	text := "RRN: " + both
	assert.Len(t, scanOne(t, TypeKRRRN, text), 1)
	fs := scanAll(t, text)
	require.Len(t, fs, 1)
	assert.Equal(t, TypeCreditCard, fs[0].Type)
}

func TestResolve_Overlaps_Deterministic(t *testing.T) {
	t.Parallel()
	mk := func(typ string, s, e int, c class) candidate {
		return candidate{privacy.Finding{DetectorID: typ, Type: typ, Start: s, End: e}, c}
	}
	cases := []struct {
		name  string
		cands []candidate
		want  []string
	}{
		{"empty", nil, nil},
		{"class beats length", []candidate{mk("ctx", 0, 20, classContext), mk("chk", 5, 10, classChecksum)}, []string{"chk"}},
		{
			"structural beats context",
			[]candidate{mk("ctx", 0, 10, classContext), mk("str", 0, 8, classStructural)},
			[]string{"str"},
		},
		{"longer wins in class", []candidate{mk("a", 0, 5, classStructural), mk("b", 3, 12, classStructural)}, []string{"b"}},
		{
			"tie by start then type",
			[]candidate{mk("b", 5, 10, classContext), mk("a", 0, 5, classContext)},
			[]string{"a", "b"},
		},
		{
			"identical span ordered by type",
			[]candidate{mk("z", 0, 5, classContext), mk("m", 0, 5, classContext)},
			[]string{"m"},
		},
		{
			"disjoint sorted by start",
			[]candidate{mk("late", 10, 12, classChecksum), mk("early", 0, 2, classHeuristic)},
			[]string{"early", "late"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, f := range resolve(tc.cands) {
				got = append(got, f.Type)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDetector_Detect_ContextCancelled(t *testing.T) {
	t.Parallel()
	d, err := New(WithStreetAddress(true))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fs, err := d.Detect(ctx, "jo@example.com")
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, fs)
	for _, sub := range d.Detectors() {
		_, err := sub.Detect(ctx, "jo@example.com")
		require.ErrorIs(t, err, context.Canceled, sub.ID())
	}
}

func TestDetector_Detect_EmptyAndBenignText(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "hello world", "1 2 3", strings.Repeat("a", 5000)} {
		assert.Empty(t, scanAll(t, text))
	}
}

func TestDetector_Detect_ConcurrentUse(t *testing.T) {
	t.Parallel()
	d, err := New()
	require.NoError(t, err)
	done := make(chan []string, 8)
	for range 8 {
		go func() {
			fs, _ := d.Detect(context.Background(), "mail jo@example.com or call +84 912 345 678")
			done <- values(fs)
		}()
	}
	for range 8 {
		assert.Equal(t, []string{"jo@example.com", "+84 912 345 678"}, <-done)
	}
}

func TestDetector_Detect_PathologicalInputsFinishQuickly(t *testing.T) {
	t.Parallel()
	d, err := New(WithStreetAddress(true))
	require.NoError(t, err)
	inputs := map[string]string{
		"digit groups":   strings.Repeat("1 ", 50000),
		"dash digits":    strings.Repeat("12-", 40000),
		"colons":         strings.Repeat(":", 100000),
		"hex and colons": strings.Repeat("ab:", 40000),
		"plus digits":    strings.Repeat("+1 2 ", 30000),
		"long digits":    strings.Repeat("7", 100000),
		"name words":     "1" + strings.Repeat(" Abc", 30000),
		"at signs":       strings.Repeat("a@", 50000),
	}
	for name, text := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			fs, err := d.Detect(context.Background(), text)
			require.NoError(t, err)
			for _, f := range fs {
				require.Equal(t, text[f.Start:f.End], f.Value)
			}
			assert.Less(t, time.Since(start), 20*time.Second)
		})
	}
}

func TestNew_WithDisabled(t *testing.T) {
	t.Parallel()
	all, err := New(WithStreetAddress(true))
	require.NoError(t, err)
	assert.Len(t, all.Detectors(), len(IDs()))
	d, err := New(WithDisabled(TypeEmail, TypePhone))
	require.NoError(t, err)
	for _, sub := range d.Detectors() {
		assert.NotContains(t, []string{TypeEmail, TypePhone}, sub.ID())
	}
	fs, err := d.Detect(context.Background(), "mail a.b@example.com")
	require.NoError(t, err)
	assert.Empty(t, fs)
	_, err = New(WithDisabled("emial"))
	require.ErrorContains(t, err, "unknown detector")
}
