// SPDX-License-Identifier: Apache-2.0

package difficulty_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
)

func TestScore_Validate_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		p       float64
		wantErr bool
	}{
		{name: "zero", p: 0},
		{name: "one", p: 1},
		{name: "middle", p: 0.5},
		{name: "negative", p: -0.01, wantErr: true},
		{name: "above one", p: 1.01, wantErr: true},
		{name: "NaN", p: math.NaN(), wantErr: true},
		{name: "plus inf", p: math.Inf(1), wantErr: true},
		{name: "minus inf", p: math.Inf(-1), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := difficulty.Score{P: tc.p}.Validate()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "difficulty: score")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestThreshold_Tier_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		th   difficulty.Threshold
		p    float64
		want model.Tier
	}{
		{name: "below", th: 0.62, p: 0.61, want: model.TierWeak},
		{name: "equal is strong", th: 0.62, p: 0.62, want: model.TierStrong},
		{name: "above", th: 0.62, p: 0.9, want: model.TierStrong},
		{name: "zero p", th: 0.5, p: 0, want: model.TierWeak},
		{name: "threshold zero always strong", th: 0, p: 0, want: model.TierStrong},
		{name: "threshold one needs p one", th: 1, p: 0.999, want: model.TierWeak},
		{name: "threshold one and p one", th: 1, p: 1, want: model.TierStrong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.th.Tier(tc.p))
		})
	}
}

func TestScoredText_Request_Table(t *testing.T) {
	t.Parallel()
	text := func(s string) chat.Part { return chat.Part{Type: chat.PartText, Text: s} }
	img := chat.Part{Type: chat.PartImage, ImageURL: "u"}
	tests := []struct {
		name    string
		msgs    []chat.Message
		want    string
		wantErr error
	}{
		{name: "no messages", msgs: nil, wantErr: difficulty.ErrNoScoredText},
		{
			name:    "no user message",
			msgs:    []chat.Message{chat.TextMessage(chat.RoleSystem, "s")},
			wantErr: difficulty.ErrNoScoredText,
		},
		{name: "single text", msgs: []chat.Message{chat.TextMessage(chat.RoleUser, "hello")}, want: "hello"},
		{
			name: "multi part joined by newline",
			msgs: []chat.Message{{Role: chat.RoleUser, Content: []chat.Part{text("a"), text("b"), text("c")}}},
			want: "a\nb\nc",
		},
		{
			name: "image between texts skipped",
			msgs: []chat.Message{{Role: chat.RoleUser, Content: []chat.Part{text("a"), img, text("b")}}},
			want: "a\nb",
		},
		{
			name: "image only last user message",
			msgs: []chat.Message{
				chat.TextMessage(chat.RoleUser, "earlier"),
				{Role: chat.RoleUser, Content: []chat.Part{img}},
			},
			want: "",
		},
		{
			name: "last user not last message",
			msgs: []chat.Message{
				chat.TextMessage(chat.RoleUser, "first"),
				chat.TextMessage(chat.RoleUser, "second"),
				chat.TextMessage(chat.RoleAssistant, "reply"),
			},
			want: "second",
		},
		{
			name: "user message without content",
			msgs: []chat.Message{{Role: chat.RoleUser}},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := difficulty.ScoredText(chat.Request{Messages: tc.msgs})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
