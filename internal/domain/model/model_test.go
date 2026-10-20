// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
)

func TestLocality_Valid_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		l    model.Locality
		want bool
	}{
		{name: "local", l: model.LocalityLocal, want: true},
		{name: "cloud", l: model.LocalityCloud, want: true},
		{name: "empty", l: "", want: false},
		{name: "unknown", l: "edge", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.l.Valid())
		})
	}
}

func TestTier_Valid_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tier model.Tier
		want bool
	}{
		{name: "weak", tier: model.TierWeak, want: true},
		{name: "strong", tier: model.TierStrong, want: true},
		{name: "none", tier: model.TierNone, want: false},
		{name: "unknown", tier: "medium", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.tier.Valid())
		})
	}
}

func TestTier_Other_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tier model.Tier
		want model.Tier
	}{
		{name: "weak to strong", tier: model.TierWeak, want: model.TierStrong},
		{name: "strong to weak", tier: model.TierStrong, want: model.TierWeak},
		{name: "none stays none", tier: model.TierNone, want: model.TierNone},
		{name: "unknown becomes none", tier: "medium", want: model.TierNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.tier.Other())
		})
	}
}

func TestCapability_Valid_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    model.Capability
		want bool
	}{
		{name: "tools", c: model.CapTools, want: true},
		{name: "vision", c: model.CapVision, want: true},
		{name: "json mode", c: model.CapJSONMode, want: true},
		{name: "embeddings", c: model.CapEmbeddings, want: true},
		{name: "unknown", c: "telepathy", want: false},
		{name: "empty", c: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.c.Valid())
		})
	}
}

func TestPricing_Cost_Table(t *testing.T) {
	t.Parallel()
	p := model.Pricing{InputPerMTok: 2, OutputPerMTok: 8, CachedInputPerMTok: 0.5}
	tests := []struct {
		name    string
		pricing model.Pricing
		u       chat.Usage
		want    float64
	}{
		{name: "zero usage", pricing: p, u: chat.Usage{}, want: 0},
		{name: "input only", pricing: p, u: chat.Usage{PromptTokens: 1_000_000}, want: 2},
		{name: "output only", pricing: p, u: chat.Usage{CompletionTokens: 500_000}, want: 4},
		{
			name:    "cached billed at cached rate",
			pricing: p,
			u:       chat.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, CachedPromptTokens: 400_000},
			// 600k*2 + 400k*0.5 + 1M*8 = 1.2 + 0.2 + 8
			want: 9.4,
		},
		{
			name:    "cached above prompt is clamped",
			pricing: p,
			u:       chat.Usage{PromptTokens: 1_000_000, CachedPromptTokens: 5_000_000},
			want:    0.5,
		},
		{name: "free model", pricing: model.Pricing{}, u: chat.Usage{PromptTokens: 100, CompletionTokens: 100}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tc.want, tc.pricing.Cost(tc.u), 1e-9)
		})
	}
}

func TestPricing_Cost_NonNegativeAndLinear(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		p := model.Pricing{
			InputPerMTok:       rapid.Float64Range(0, 100).Draw(rt, "in"),
			OutputPerMTok:      rapid.Float64Range(0, 100).Draw(rt, "out"),
			CachedInputPerMTok: rapid.Float64Range(0, 100).Draw(rt, "cached"),
		}
		u := chat.Usage{
			PromptTokens:       rapid.IntRange(0, 100_000).Draw(rt, "prompt"),
			CompletionTokens:   rapid.IntRange(0, 100_000).Draw(rt, "completion"),
			CachedPromptTokens: rapid.IntRange(0, 200_000).Draw(rt, "cachedTokens"),
		}
		k := rapid.IntRange(1, 10).Draw(rt, "k")
		scaled := chat.Usage{
			PromptTokens:       u.PromptTokens * k,
			CompletionTokens:   u.CompletionTokens * k,
			CachedPromptTokens: u.CachedPromptTokens * k,
		}
		base := p.Cost(u)
		if base < 0 {
			rt.Fatalf("negative cost %v", base)
		}
		if diff := p.Cost(scaled) - float64(k)*base; diff > 1e-6 || diff < -1e-6 {
			rt.Fatalf("not linear: diff %v", diff)
		}
	})
}

func TestTarget_Has_Table(t *testing.T) {
	t.Parallel()
	tgt := model.Target{Capabilities: []model.Capability{model.CapTools, model.CapVision}}
	tests := []struct {
		name string
		t    model.Target
		c    model.Capability
		want bool
	}{
		{name: "present", t: tgt, c: model.CapVision, want: true},
		{name: "absent", t: tgt, c: model.CapJSONMode, want: false},
		{name: "no capabilities", t: model.Target{}, c: model.CapTools, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.t.Has(tc.c))
		})
	}
}

func TestTarget_IsEmbedding_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		t    model.Target
		want bool
	}{
		{name: "embedding", t: model.Target{Capabilities: []model.Capability{model.CapEmbeddings}}, want: true},
		{name: "chat", t: model.Target{Capabilities: []model.Capability{model.CapTools}}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.t.IsEmbedding())
		})
	}
}

func TestRequiredCapabilities_Request_Table(t *testing.T) {
	t.Parallel()
	img := chat.Message{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartImage, ImageURL: "u"}}}
	tests := []struct {
		name string
		req  chat.Request
		want []model.Capability
	}{
		{name: "plain", req: chat.Request{}, want: nil},
		{
			name: "tools",
			req:  chat.Request{Tools: []chat.Tool{{Type: "function"}}},
			want: []model.Capability{model.CapTools},
		},
		{
			name: "vision",
			req:  chat.Request{Messages: []chat.Message{img}},
			want: []model.Capability{model.CapVision},
		},
		{
			name: "json mode",
			req:  chat.Request{ResponseFormat: json.RawMessage(`{"type":"json_object"}`)},
			want: []model.Capability{model.CapJSONMode},
		},
		{name: "response format null", req: chat.Request{ResponseFormat: json.RawMessage(`null`)}, want: nil},
		{
			name: "all three in order",
			req: chat.Request{
				Messages:       []chat.Message{img},
				Tools:          []chat.Tool{{Type: "function"}},
				ResponseFormat: json.RawMessage(`{"type":"json_object"}`),
			},
			want: []model.Capability{model.CapTools, model.CapVision, model.CapJSONMode},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, model.RequiredCapabilities(tc.req))
		})
	}
}

func TestPool_ByName_Table(t *testing.T) {
	t.Parallel()
	pool := model.Pool{{Name: "a", Model: "ma"}, {Name: "b", Model: "mb"}}
	tests := []struct {
		name   string
		lookup string
		want   model.Target
		found  bool
	}{
		{name: "first", lookup: "a", want: pool[0], found: true},
		{name: "second", lookup: "b", want: pool[1], found: true},
		{name: "missing", lookup: "c", want: model.Target{}, found: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := pool.ByName(tc.lookup)
			assert.Equal(t, tc.found, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPool_Filter_Table(t *testing.T) {
	t.Parallel()
	pool := model.Pool{
		{Name: "a", Tier: model.TierWeak},
		{Name: "b", Tier: model.TierStrong},
		{Name: "c", Tier: model.TierWeak},
	}
	tests := []struct {
		name string
		keep func(model.Target) bool
		want []string
	}{
		{
			name: "weak keeps order",
			keep: func(t model.Target) bool { return t.Tier == model.TierWeak },
			want: []string{"a", "c"},
		},
		{name: "none", keep: func(model.Target) bool { return false }, want: []string{}},
		{name: "all", keep: func(model.Target) bool { return true }, want: []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := pool.Filter(tc.keep)
			names := make([]string, 0, len(got))
			for _, tg := range got {
				names = append(names, tg.Name)
			}
			assert.Equal(t, tc.want, names)
		})
	}
	assert.Len(t, pool, 3, "filter must not modify the source pool")
}

func TestLocalitySet_Allows_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		set  model.LocalitySet
		loc  model.Locality
		want bool
	}{
		{name: "all allows local", set: model.AllLocalities(), loc: model.LocalityLocal, want: true},
		{name: "all allows cloud", set: model.AllLocalities(), loc: model.LocalityCloud, want: true},
		{name: "local only allows local", set: model.LocalOnly(), loc: model.LocalityLocal, want: true},
		{name: "local only denies cloud", set: model.LocalOnly(), loc: model.LocalityCloud, want: false},
		{name: "unknown locality denied", set: model.AllLocalities(), loc: "edge", want: false},
		{name: "empty set denies", set: model.LocalitySet{}, loc: model.LocalityLocal, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.set.Allows(tc.loc))
		})
	}
}
