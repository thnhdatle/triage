// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"unicode/utf8"
)

// runMatcher is the regex prefilter. Go's regexp tries every start position
// of an unanchored search, which on a 2 KB prompt costs hundreds of
// microseconds per pattern even when nothing matches. Every pattern used here
// matches only ASCII bytes from a fixed set (its alphabet) and has no
// assertions, so each match lies inside one maximal run of alphabet bytes,
// and the leftmost-first matches of the whole text are exactly the matches of
// each run, in order. runMatcher runs the regexp only on runs that can hold a
// match: runs at least as long as the shortest match, with at least as many
// ASCII digits and required bytes as every match must contain. Prose
// therefore costs one byte loop instead of a regexp search. The alphabet and
// the lower bounds are derived from the pattern itself, so they cannot drift
// from it; newRunMatcher panics for a pattern the reasoning does not cover.
type runMatcher struct {
	re *regexp.Regexp
	// alphabet has 256 entries so the scan loop needs no range check; bytes
	// >= utf8.RuneSelf are never set.
	alphabet  [256]bool
	minLen    int
	minDigits int
	// need is the required byte (0 = none) and needN its minimum count.
	need  byte
	needN int
}

// newRunMatcher compiles pattern and derives its prefilter. need, when not 0,
// is a byte every match contains (checked against the pattern).
func newRunMatcher(pattern string, need byte) *runMatcher {
	re := regexp.MustCompile(pattern)
	ast, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		panic(fmt.Sprintf("piiregex: parse %q: %v", pattern, err))
	}
	ast = ast.Simplify()
	m := &runMatcher{re: re, need: need}
	if err := collectAlphabet(ast, &m.alphabet); err != nil {
		panic(fmt.Sprintf("piiregex: pattern %q cannot use the run prefilter: %v", pattern, err))
	}
	m.minLen = minCount(ast, func(lo, hi rune) bool { return true })
	m.minDigits = minCount(ast, func(lo, hi rune) bool { return lo >= '0' && hi <= '9' })
	if need != 0 {
		m.needN = minCount(ast, func(lo, hi rune) bool { return lo == rune(need) && hi == rune(need) })
		if m.needN == 0 {
			panic(fmt.Sprintf("piiregex: pattern %q does not require %q", pattern, need))
		}
	}
	if m.minLen == 0 {
		panic(fmt.Sprintf("piiregex: pattern %q can match the empty string", pattern))
	}
	return m
}

// collectAlphabet adds every byte a match of re can contain to set. It fails
// for non-ASCII runes, any-character nodes, assertions and case folding.
func collectAlphabet(re *syntax.Regexp, set *[256]bool) error {
	if re.Flags&syntax.FoldCase != 0 && (re.Op == syntax.OpLiteral || re.Op == syntax.OpCharClass) {
		return fmt.Errorf("case-folded %s", re)
	}
	switch re.Op {
	case syntax.OpLiteral:
		return addRanges(set, re.Rune, true)
	case syntax.OpCharClass:
		return addRanges(set, re.Rune, false)
	case syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat,
		syntax.OpConcat, syntax.OpAlternate:
		for _, s := range re.Sub {
			if err := collectAlphabet(s, set); err != nil {
				return err
			}
		}
		return nil
	case syntax.OpEmptyMatch:
		return nil
	case syntax.OpNoMatch, syntax.OpAnyCharNotNL, syntax.OpAnyChar, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return fmt.Errorf("unsupported node %s (op %d)", re, re.Op)
	default:
		return fmt.Errorf("unknown node %s (op %d)", re, re.Op)
	}
}

// addRanges adds runes (literal: each rune; otherwise lo-hi pairs) to set,
// failing for any rune outside ASCII.
func addRanges(set *[256]bool, runes []rune, literal bool) error {
	step := 2
	if literal {
		step = 1
	}
	for i := 0; i+step-1 < len(runes); i += step {
		lo, hi := runes[i], runes[i+step-1]
		if hi >= utf8.RuneSelf {
			return fmt.Errorf("rune range %q-%q is not ASCII", lo, hi)
		}
		for r := lo; r <= hi; r++ {
			set[r] = true
		}
	}
	return nil
}

// minCount is a lower bound on the number of bytes of any match of re that
// come from a node whose every class range satisfies in (every literal rune
// r is the range r-r).
func minCount(re *syntax.Regexp, in func(lo, hi rune) bool) int {
	switch re.Op {
	case syntax.OpLiteral:
		n := 0
		for _, r := range re.Rune {
			if in(r, r) {
				n++
			}
		}
		return n
	case syntax.OpCharClass:
		return classCount(re.Rune, in)
	case syntax.OpCapture, syntax.OpPlus:
		return minCount(re.Sub[0], in)
	case syntax.OpRepeat:
		return re.Min * minCount(re.Sub[0], in)
	case syntax.OpConcat:
		n := 0
		for _, s := range re.Sub {
			n += minCount(s, in)
		}
		return n
	case syntax.OpAlternate:
		n := minCount(re.Sub[0], in)
		for _, s := range re.Sub[1:] {
			n = min(n, minCount(s, in))
		}
		return n
	case syntax.OpStar, syntax.OpQuest, syntax.OpEmptyMatch, syntax.OpNoMatch, syntax.OpAnyCharNotNL,
		syntax.OpAnyChar, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0
	default:
		return 0
	}
}

// classCount is 1 when every range of a character class satisfies in, else 0.
func classCount(ranges []rune, in func(lo, hi rune) bool) int {
	if len(ranges) == 0 {
		return 0
	}
	for i := 0; i+1 < len(ranges); i += 2 {
		if !in(ranges[i], ranges[i+1]) {
			return 0
		}
	}
	return 1
}

// findAll returns the same indices as re.FindAllStringIndex(text, -1).
func (m *runMatcher) findAll(text string) [][]int {
	var out [][]int
	for i := 0; i < len(text); {
		if !m.alphabet[text[i]] {
			i++
			continue
		}
		j, digits, need := i, 0, 0
		for ; j < len(text) && m.alphabet[text[j]]; j++ {
			if isDigit(text[j]) {
				digits++
			}
			if text[j] == m.need {
				need++
			}
		}
		if j-i >= m.minLen && digits >= m.minDigits && need >= m.needN {
			for _, loc := range m.re.FindAllStringIndex(text[i:j], -1) {
				out = append(out, []int{i + loc[0], i + loc[1]})
			}
		}
		i = j
	}
	return out
}
