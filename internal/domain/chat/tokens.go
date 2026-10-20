// SPDX-License-Identifier: Apache-2.0

package chat

import "unicode/utf8"

// TokenCounter counts the tokens of a text. The default is EstimateTokens;
// exact counters load a tokenizer.json.
type TokenCounter interface {
	CountTokens(text string) int
}

// EstimateTokens is the UTF-8 aware estimate
// ceil(ascii_bytes/4 + non_ascii_bytes/3).
func EstimateTokens(text string) int {
	ascii := 0
	for i := 0; i < len(text); i++ {
		if text[i] < utf8.RuneSelf {
			ascii++
		}
	}
	nonASCII := len(text) - ascii
	// ceil(a/4 + n/3) == ceil((3a + 4n) / 12)
	return (3*ascii + 4*nonASCII + 11) / 12
}

// Estimator is the TokenCounter backed by EstimateTokens.
type Estimator struct{}

// CountTokens implements TokenCounter.
func (Estimator) CountTokens(text string) int { return EstimateTokens(text) }

// PromptText returns the text the prompt token count is computed on: the text
// parts and tool-call arguments of every message, in order, newline separated.
func (r Request) PromptText() string {
	n := 0
	for _, m := range r.Messages {
		for _, p := range m.Content {
			n += len(p.Text) + 1
		}
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Arguments) + 1
		}
	}
	b := make([]byte, 0, n)
	for _, m := range r.Messages {
		for _, p := range m.Content {
			if p.Type == PartText {
				b = append(b, p.Text...)
				b = append(b, '\n')
			}
		}
		for _, tc := range m.ToolCalls {
			b = append(b, tc.Function.Arguments...)
			b = append(b, '\n')
		}
	}
	return string(b)
}

// PromptTokens counts the prompt tokens of the request with the counter.
func (r Request) PromptTokens(c TokenCounter) int {
	if c == nil {
		c = Estimator{}
	}
	return c.CountTokens(r.PromptText())
}
