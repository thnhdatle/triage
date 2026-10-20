// SPDX-License-Identifier: Apache-2.0

package hftok_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// benchText repeats unit until the bert encoding has at least 512 ids
// (incl. <s> and </s>); the same text is used for the ner benchmarks.
func benchText(b *testing.B, unit string) string {
	b.Helper()
	tok := loadPinned(b, "bert")
	text := ""
	for {
		text += unit
		ids, err := tok.Encode(text, true)
		require.NoError(b, err)
		if len(ids) >= 512 {
			return text
		}
	}
}

func BenchmarkEncode_512Tokens(b *testing.B) {
	units := []struct {
		name string
		unit string
	}{
		{name: "en", unit: "The quick brown fox jumps over the lazy dog near Boston. "},
		{name: "ko", unit: "인공지능 기술은 최근 빠르게 발전해 왔습니다. "},
		{name: "mixed", unit: "Emoji 😀 👨\u200d👩\u200d👧 ｆｕｌｌ ﬁ cafe\u0301 中文 "},
	}
	for _, name := range []string{"bert", "ner"} {
		for _, u := range units {
			text := benchText(b, u.unit)
			tok := loadPinned(b, name)
			b.Run(name+"/"+u.name, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				for b.Loop() {
					if _, err := tok.Encode(text, true); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
