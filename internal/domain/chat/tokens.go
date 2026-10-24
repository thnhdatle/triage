// SPDX-License-Identifier: Apache-2.0

package chat

import "unicode/utf8"

type TokenCounter interface {
	CountTokens(text string) int
}

func EstimateTokens(text string) int {
	ascii := 0
	for i := 0; i < len(text); i++ {
		if text[i] < utf8.RuneSelf {
			ascii++
		}
	}
	nonASCII := len(text) - ascii
	return (3*ascii + 4*nonASCII + 11) / 12
}

type Estimator struct{}

func (Estimator) CountTokens(text string) int { return EstimateTokens(text) }

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

func (r Request) PromptTokens(c TokenCounter) int {
	if c == nil {
		c = Estimator{}
	}
	return c.CountTokens(r.PromptText())
}
