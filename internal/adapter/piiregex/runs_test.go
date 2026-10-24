// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func runMatchers() map[string]*runMatcher {
	return map[string]*runMatcher{
		"cccd": cccdRe, "iban": ibanHeadRe, "credit_card": creditCardRe, "rrn": rrnRe, "ssn": ssnRe,
		"email": emailRe, "secret": secretRe, "ipv4": ipv4Re, "ipv6": ipv6Re, "phone_e164": phoneE164Re,
		"phone_kr": phoneKRRe, "phone_vn": phoneVNRe, "phone_us": phoneUSRe,
	}
}

func requireSameMatches(t require.TestingT, text string) {
	for name, m := range runMatchers() {
		want := m.re.FindAllStringIndex(text, -1)
		got := m.findAll(text)
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		require.Equal(t, want, got, "%s on %q", name, text)
	}
}

func TestRunMatcher_SameMatchesOnTestData(t *testing.T) {
	t.Parallel()
	n := 0
	for _, file := range []string{"labelled.jsonl", "negatives.jsonl"} {
		for _, s := range loadSamples(t, file) {
			requireSameMatches(t, s.Text)
			n++
		}
	}
	requireSameMatches(t, prompt2KB())
	requireSameMatches(t, prose2KB())
	assert.Greater(t, n, 2000)
}

func TestRunMatcher_SameMatchesProperty(t *testing.T) {
	t.Parallel()
	pieces := []string{
		"0", "1", "2", "3", "4", "5", "7", "9", "0", "1", "8", " ", "-", ".", "(", ")", "+", ":", "@", "_", "/",
		"%", "a", "e", "f", "x", "k", "s", "A", "B", "D", "E", "F", "G", "K", "I", "J", "y", "o", "b", "p",
		"sk-", "AKIA", "ghp_", "xoxb-", "eyJ", "example", ".com", "\n", "\t", "#", ",", "é", "한", "\xff",
	}
	rapid.Check(t, func(rt *rapid.T) {
		parts := rapid.SliceOfN(rapid.SampledFrom(pieces), 0, 120).Draw(rt, "parts")
		requireSameMatches(rt, strings.Join(parts, ""))
	})
}

func FuzzRunMatcher(f *testing.F) {
	for _, seed := range []string{
		"", "4111 1111 1111 1111", "a+1 (555) 123-4567b", "2001:db8::1 and 10.0.0.1", "x@y.zz and a@b.c",
		"DE89 3704 0044 0532 0130 00", "900101-1234567", "079203001234", "\xff1234567890123",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) { requireSameMatches(t, text) })
}

func TestRunMatcher_DerivedBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		m                     *runMatcher
		minLen, digits, needN int
	}{
		{ssnRe, 11, 9, 0},
		{rrnRe, 13, 13, 0},
		{cccdRe, 12, 12, 0},
		{creditCardRe, 13, 13, 0},
		{emailRe, 6, 0, 1},
		{ipv4Re, 7, 4, 0},
		{ipv6Re, 1, 0, 1},
		{phoneUSRe, 10, 10, 0},
		{phoneKRRe, 9, 9, 0},
		{phoneVNRe, 10, 10, 0},
		{phoneE164Re, 9, 2, 1},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.minLen, tt.m.minLen, tt.m.re.String())
		assert.Equal(t, tt.digits, tt.m.minDigits, tt.m.re.String())
		assert.Equal(t, tt.needN, tt.m.needN, tt.m.re.String())
	}
	assert.True(t, ipv6Re.alphabet[':'] && ipv6Re.alphabet['f'] && !ipv6Re.alphabet['g'])
}

func TestRunMatcher_RejectsPatternsOutsideTheArgument(t *testing.T) {
	t.Parallel()
	for _, p := range []string{`\bfoo`, `a.b`, `(?i)abc`, `é\d`, `[^a]`, `a*`, `^\d+`} {
		assert.Panics(t, func() { newRunMatcher(p, 0) }, p)
	}
	assert.Panics(t, func() { newRunMatcher(`a|b@c`, '@') }, "need byte not required by every match")
}
