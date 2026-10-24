// SPDX-License-Identifier: Apache-2.0

package eval

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// Scorer reports whether an answer matches the reference.
type Scorer func(answer, reference string) bool

const (
	ScorerGSM8K   = "gsm8k"
	ScorerMMLU    = "mmlu"
	ScorerExact   = "exact"
	ScorerRegex   = "regex"
	ScorerNumeric = "numeric"
)

func ScorerByName(name string) (Scorer, error) {
	switch name {
	case ScorerGSM8K:
		return ScoreGSM8K, nil
	case ScorerMMLU:
		return ScoreMMLU, nil
	case ScorerExact:
		return ScoreExact, nil
	case ScorerRegex:
		return ScoreRegex, nil
	case ScorerNumeric:
		return ScoreNumeric, nil
	}
	return nil, fmt.Errorf("eval: unknown scorer %q", name)
}

func ScoreExact(answer, reference string) bool {
	return strings.TrimSpace(answer) == strings.TrimSpace(reference)
}

func ScoreRegex(answer, reference string) bool {
	re, err := regexp.Compile(reference)
	if err != nil {
		return false
	}
	return re.MatchString(answer)
}

func ScoreNumeric(answer, reference string) bool { return ScoreGSM8K(answer, reference) }

var numberRE = regexp.MustCompile(`(-?)\s?\$?(-?)(\d{1,3}(?:,\d{3})+|\d+)(\.\d+)?`)

func ScoreGSM8K(answer, reference string) bool {
	a, ok := finalNumber(answer)
	if !ok {
		return false
	}
	r, ok := finalNumber(reference)
	return ok && a.Cmp(r) == 0
}

func finalNumber(text string) (*big.Rat, bool) {
	first := false
	if i := strings.LastIndex(text, "####"); i >= 0 {
		text, first = text[i+4:], true
	}
	locs := numberRE.FindAllStringSubmatchIndex(text, -1)
	for n := range locs {
		i := len(locs) - 1 - n
		if first {
			i = n
		}
		loc := locs[i]
		neg := loc[3] > loc[2] || loc[5] > loc[4]
		if loc[3] > loc[2] && loc[0] > 0 && isAlnum(text[loc[0]-1]) {
			neg = loc[5] > loc[4]
		}
		digits := strings.ReplaceAll(text[loc[6]:loc[7]], ",", "")
		if loc[8] >= 0 {
			digits += text[loc[8]:loc[9]]
		}
		v, ok := new(big.Rat).SetString(digits)
		if !ok {
			continue
		}
		if neg {
			v.Neg(v)
		}
		return v, true
	}
	return nil, false
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

var (
	mmluAnswerRE = regexp.MustCompile("(?i)\\banswer\\b(?:\\s+is)?[\\s:=\\-(\\[*\"'`]*([A-D])([^A-Za-z0-9]|$)")
	mmluMarkedRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:\(([A-D])\)|\*\*([A-D])\*\*|([A-D])(?:[.):]|$))`)
)

func ScoreMMLU(answer, reference string) bool {
	a, ok := finalLetter(answer)
	if !ok {
		return false
	}
	r, ok := finalLetter(reference)
	return ok && a == r
}

func finalLetter(text string) (byte, bool) {
	text = strings.TrimSpace(text)
	if l, ok := answerPhraseLetter(text); ok {
		return l, true
	}
	stripped := strings.Trim(text, "*_()[]\"'`.:\t\n\r ")
	if len(stripped) == 1 {
		if c := stripped[0] &^ 0x20; c >= 'A' && c <= 'D' {
			return c, true
		}
	}
	ms := mmluMarkedRE.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return 0, false
	}
	last := ms[len(ms)-1]
	for _, g := range last[1:] {
		if g != "" {
			return g[0], true
		}
	}
	return 0, false
}

func answerPhraseLetter(text string) (byte, bool) {
	locs := mmluAnswerRE.FindAllStringSubmatchIndex(text, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		start, end := locs[i][2], locs[i][3]
		c := text[start]
		if c >= 'a' && c <= 'd' && followedByWord(text[end:]) {
			continue
		}
		return c &^ 0x20, true
	}
	return 0, false
}

func followedByWord(rest string) bool {
	rest = strings.TrimLeft(rest, " \t")
	return rest != "" && (rest[0] >= 'a' && rest[0] <= 'z' || rest[0] >= 'A' && rest[0] <= 'Z')
}
