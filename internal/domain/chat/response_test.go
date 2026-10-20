// SPDX-License-Identifier: Apache-2.0

package chat_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func TestUsage_TotalTokens_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		u    chat.Usage
		want int
	}{
		{name: "zero", u: chat.Usage{}, want: 0},
		{name: "sum ignores cached", u: chat.Usage{PromptTokens: 10, CompletionTokens: 5, CachedPromptTokens: 4}, want: 15},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.u.TotalTokens())
		})
	}
}

func TestUsage_Add_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b chat.Usage
		want chat.Usage
	}{
		{name: "zeros", want: chat.Usage{}},
		{
			name: "fields summed",
			a:    chat.Usage{PromptTokens: 1, CompletionTokens: 2, CachedPromptTokens: 3},
			b:    chat.Usage{PromptTokens: 10, CompletionTokens: 20, CachedPromptTokens: 30},
			want: chat.Usage{PromptTokens: 11, CompletionTokens: 22, CachedPromptTokens: 33},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.a.Add(tc.b))
		})
	}
}
