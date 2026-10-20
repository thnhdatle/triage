// SPDX-License-Identifier: Apache-2.0

package eval_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/eval"
)

type scoreCase struct {
	name      string
	answer    string
	reference string
	want      bool
}

func runScoreCases(t *testing.T, score eval.Scorer, tests []scoreCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, score(tc.answer, tc.reference))
		})
	}
}

func TestScorer_ScoreGSM8K_Cases(t *testing.T) {
	t.Parallel()
	runScoreCases(t, eval.ScoreGSM8K, []scoreCase{
		{name: "plain", answer: "18", reference: "18", want: true},
		{name: "last number wins", answer: "2 + 16 = 18", reference: "18", want: true},
		{name: "commas", answer: "The total is 1,234 apples", reference: "1234", want: true},
		{name: "dollar", answer: "She earns $18.", reference: "18", want: true},
		{name: "units", answer: "He has 12 dollars", reference: "12", want: true},
		{name: "marker in answer", answer: "blah 5 blah\n#### 42", reference: "42", want: true},
		{name: "marker in reference", answer: "The answer is 18.", reference: "step 1: 3*6\n#### 18", want: true},
		{name: "marker ignores later text", answer: "#### 7\nhope that helps 100", reference: "7", want: true},
		{name: "trailing period", answer: "It is 7.", reference: "7", want: true},
		{name: "negative", answer: "The change is -5", reference: "-5", want: true},
		{name: "negative after dollar", answer: "balance: $-5", reference: "-5", want: true},
		{name: "negative mismatch", answer: "5", reference: "-5", want: false},
		{name: "hyphen not sign", answer: "between 3-4", reference: "4", want: true},
		{name: "decimal equal", answer: "3.50", reference: "3.5", want: true},
		{name: "decimal differs", answer: "3.51", reference: "3.5", want: false},
		{name: "big integer exact", answer: "9007199254740993", reference: "9007199254740992", want: false},
		{name: "wrong", answer: "19", reference: "18", want: false},
		{name: "no number in answer", answer: "I do not know", reference: "18", want: false},
		{name: "no number in reference", answer: "18", reference: "none", want: false},
		{name: "empty", answer: "", reference: "", want: false},
		{name: "comma list splits", answer: "1,2", reference: "2", want: true},
	})
}

func TestScorer_ScoreMMLU_Cases(t *testing.T) {
	t.Parallel()
	runScoreCases(t, eval.ScoreMMLU, []scoreCase{
		{name: "answer is paren", answer: "The answer is (B)", reference: "B", want: true},
		{name: "answer is plain", answer: "So the answer is B.", reference: "B", want: true},
		{name: "dot", answer: "B.", reference: "B", want: true},
		{name: "bold", answer: "**B**", reference: "B", want: true},
		{name: "answer colon lower", answer: "Answer: b", reference: "B", want: true},
		{name: "bold answer", answer: "**Answer:** C", reference: "C", want: true},
		{name: "bare lower", answer: "d", reference: "D", want: true},
		{name: "last answer phrase wins", answer: "Answer: A ... on reflection the answer is C", reference: "C", want: true},
		{name: "article is not a choice", answer: "The answer is a dog, so B.", reference: "B", want: true},
		{name: "marked option", answer: "Options A and B are wrong.\nThe correct one is (D)", reference: "D", want: true},
		{name: "bold dotted option", answer: "**C. Paris**", reference: "C", want: true},
		{name: "final letter", answer: "I pick B", reference: "B", want: true},
		{name: "reference with phrase", answer: "(A)", reference: "The answer is A", want: true},
		{name: "article A ignored", answer: "A cat sat on a mat", reference: "A", want: false},
		{name: "mismatch", answer: "The answer is (B)", reference: "C", want: false},
		{name: "out of range letter", answer: "The answer is E", reference: "E", want: false},
		{name: "no letter", answer: "I cannot say", reference: "B", want: false},
		{name: "no reference letter", answer: "B", reference: "42", want: false},
		{name: "empty", answer: "", reference: "", want: false},
	})
}

func TestScorer_ScoreExact_Cases(t *testing.T) {
	t.Parallel()
	runScoreCases(t, eval.ScoreExact, []scoreCase{
		{name: "trimmed", answer: "  hi\n", reference: "hi", want: true},
		{name: "case sensitive", answer: "Hi", reference: "hi", want: false},
		{name: "different", answer: "hi there", reference: "hi", want: false},
	})
}

func TestScorer_ScoreRegex_Cases(t *testing.T) {
	t.Parallel()
	runScoreCases(t, eval.ScoreRegex, []scoreCase{
		{name: "matches", answer: "the code is 4521", reference: `\d{4}`, want: true},
		{name: "anchored fails", answer: "the code is 4521", reference: `^\d{4}$`, want: false},
		{name: "invalid regex", answer: "x", reference: `(`, want: false},
	})
}

func TestScorer_ScoreNumeric_Cases(t *testing.T) {
	t.Parallel()
	runScoreCases(t, eval.ScoreNumeric, []scoreCase{
		{name: "equal", answer: "about 1,000.0 units", reference: "1000", want: true},
		{name: "differs", answer: "999", reference: "1000", want: false},
	})
}

func TestScorer_ScorerByName_Names(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"gsm8k", "mmlu", "exact", "regex", "numeric"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, err := eval.ScorerByName(name)
			require.NoError(t, err)
			assert.NotNil(t, s)
		})
	}
	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		s, err := eval.ScorerByName("bleu")
		require.Error(t, err)
		assert.Nil(t, s)
	})
}
