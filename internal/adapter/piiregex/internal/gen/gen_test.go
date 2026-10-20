// SPDX-License-Identifier: Apache-2.0

package gen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerator_Write_Deterministic(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	require.NoError(t, Write(a, Seed))
	require.NoError(t, Write(b, Seed))
	for _, name := range []string{LabelledFile, NegativesFile} {
		x, err := os.ReadFile(filepath.Join(a, name))
		require.NoError(t, err)
		y, err := os.ReadFile(filepath.Join(b, name))
		require.NoError(t, err)
		assert.Equal(t, x, y)
	}
	other := t.TempDir()
	require.NoError(t, Write(other, Seed+1))
	x, _ := os.ReadFile(filepath.Join(a, LabelledFile))
	y, _ := os.ReadFile(filepath.Join(other, LabelledFile))
	assert.NotEqual(t, x, y)
}

func TestGenerator_Write_BadDirectory(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	require.Error(t, Write(filepath.Join(file, "sub"), Seed))
	require.Error(t, writeJSONL(filepath.Join(t.TempDir(), "missing", "x.jsonl"), nil))
}

func TestGenerator_Positives_Labels(t *testing.T) {
	t.Parallel()
	ps := Positives(Seed)
	require.GreaterOrEqual(t, len(ps), 1000)
	perType := map[string]int{}
	langs := map[string]int{}
	var hangul, vietnamese bool
	for _, s := range ps {
		langs[s.Lang]++
		require.NotEmpty(t, s.Spans)
		for _, sp := range s.Spans {
			require.True(t, 0 <= sp.Start && sp.Start < sp.End && sp.End <= len(s.Text), s.Text)
			perType[sp.Type]++
		}
		for _, r := range s.Text {
			hangul = hangul || unicode.Is(unicode.Hangul, r)
			vietnamese = vietnamese || r == 'ệ' || r == 'ư'
		}
	}
	for _, typ := range strings.Split(AllTypes, "|") {
		assert.GreaterOrEqual(t, perType[typ], PositivesPerType, typ)
	}
	assert.Len(t, langs, 3)
	assert.True(t, hangul && vietnamese)
}

func TestGenerator_Positives_IncludePost2020RRN(t *testing.T) {
	t.Parallel()
	count := 0
	for _, s := range Positives(Seed) {
		for _, sp := range s.Spans {
			if sp.Type != "kr_rrn" {
				continue
			}
			d := strings.NewReplacer("-", "", " ", "").Replace(s.Text[sp.Start:sp.End])
			if len(d) == 13 && d[0] == '2' && d[1] >= '1' && (d[6] == '3' || d[6] == '4') {
				count++
			}
		}
	}
	assert.Greater(t, count, 10)
}

func TestGenerator_Negatives_HaveNoSpans(t *testing.T) {
	t.Parallel()
	ns := Negatives(Seed)
	require.Len(t, ns, NegativeCount)
	seen := map[string]bool{}
	for _, s := range ns {
		assert.NotNil(t, s.Spans)
		assert.Empty(t, s.Spans)
		assert.NotEmpty(t, s.Text)
		seen[s.Text] = true
	}
	assert.Greater(t, len(seen), 900, "negatives should be varied")
}

func TestGenerator_Values_Validity(t *testing.T) {
	t.Parallel()
	g := newGenerator(1)
	luhn := regexp.MustCompile(`^\d{13,19}$`)
	for range 200 {
		cc := strings.NewReplacer(" ", "", "-", "").Replace(g.creditCard())
		require.Regexp(t, luhn, cc)
		assert.Equal(t, 0, luhnCheck(cc[:len(cc)-1])-int(cc[len(cc)-1]-'0'))
		d := g.rrn(true)
		assert.Equal(t, int(d[12]-'0'), rrnCheck(d[:12]))
		d = g.rrn(false)
		assert.NotEqual(t, int(d[12]-'0'), rrnCheck(d[:12]))
	}
	assert.Equal(t, "0124", bumpLastDigit("0123"))
	assert.Equal(t, "ab0", bumpLastDigit("ab9"))
}
