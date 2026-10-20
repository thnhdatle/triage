// SPDX-License-Identifier: Apache-2.0

package eval

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// Scorer decides whether a model answer is correct given the item's reference.
type Scorer func(answer, reference string) bool

// Scorer names accepted by ScorerByName.
const (
	ScorerGSM8K   = "gsm8k"
	ScorerMMLU    = "mmlu"
	ScorerExact   = "exact"
	ScorerRegex   = "regex"
	ScorerNumeric = "numeric"
)

// ScorerByName returns the scorer registered under name: gsm8k, mmlu, or the user scorers exact
// (trimmed string equality), regex (the reference is a regular expression that must match somewhere in
// the answer; an invalid expression never matches) and numeric (same as gsm8k).
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

// ScoreExact reports whether answer and reference are equal after trimming surrounding whitespace.
func ScoreExact(answer, reference string) bool {
	return strings.TrimSpace(answer) == strings.TrimSpace(reference)
}

// ScoreRegex reports whether the regular expression in reference matches answer. An invalid expression
// never matches.
func ScoreRegex(answer, reference string) bool {
	re, err := regexp.Compile(reference)
	if err != nil {
		return false
	}
	return re.MatchString(answer)
}

// ScoreNumeric is ScoreGSM8K under its user-facing name.
func ScoreNumeric(answer, reference string) bool { return ScoreGSM8K(answer, reference) }

var numberRE = regexp.MustCompile(`(-?)\s?\$?(-?)(\d{1,3}(?:,\d{3})+|\d+)(\.\d+)?`)

// ScoreGSM8K reports whether the final number of answer equals the final number of reference.
//
// The final number is the first number after the last "####" marker when there is one (GSM8K's
// answer delimiter), otherwise the last number of the whole text. A number may carry a leading minus (also
// after a "$"), thousands commas ("1,234"), a "$" prefix, a decimal part, and any trailing unit or
// period ("12 dollars", "7."). A minus glued to a preceding letter or digit ("3-4") is a hyphen, not
// a sign. Values compare exactly as rationals, so "3.50" equals "3.5" and "1,234" equals "1234". A text
// without a number never matches.
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
			neg = loc[5] > loc[4] // hyphen between words or digits, not a sign
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

// ScoreMMLU reports whether the letter chosen in answer equals the letter of reference.
//
// A letter is extracted by the first rule that applies:
//  1. the last "answer [is|:] X" phrase (case-insensitive, optional "(", "**", quotes around X), so
//     "The answer is (B)", "Answer: b" and "**Answer:** C" work; a lower-case letter followed by
//     another word ("the answer is a dog") is an article and is skipped;
//  2. the whole trimmed text is one letter once markup ("*", "_", brackets, quotes, final punctuation)
//     is stripped ("B.", "**B**", "(b)");
//  3. the last upper-case letter A-D that is marked as a choice: "(B)", "**B**", "B." "B)" "B:" or the
//     final character of the text.
//
// The reference goes through the same extraction, so it may be "B", "(B)" or "The answer is B". Text
// without a letter, or a reference without one, never matches.
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

// followedByWord reports whether rest starts with spaces and then a letter.
func followedByWord(rest string) bool {
	rest = strings.TrimLeft(rest, " \t")
	return rest != "" && (rest[0] >= 'a' && rest[0] <= 'z' || rest[0] >= 'A' && rest[0] <= 'Z')
}
