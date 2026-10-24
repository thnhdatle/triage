// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

type Locality string

const (
	LocalityLocal Locality = "local"
	LocalityCloud Locality = "cloud"
)

func (l Locality) Valid() bool { return l == LocalityLocal || l == LocalityCloud }

type Tier string

const (
	TierNone   Tier = ""
	TierWeak   Tier = "weak"
	TierStrong Tier = "strong"
)

func (t Tier) Valid() bool { return t == TierWeak || t == TierStrong }

func (t Tier) Other() Tier {
	switch t {
	case TierWeak:
		return TierStrong
	case TierStrong:
		return TierWeak
	case TierNone:
		return TierNone
	}
	return TierNone
}

type Capability string

const (
	CapTools      Capability = "tools"
	CapVision     Capability = "vision"
	CapJSONMode   Capability = "json_mode"
	CapEmbeddings Capability = "embeddings"
)

func (c Capability) Valid() bool {
	switch c {
	case CapTools, CapVision, CapJSONMode, CapEmbeddings:
		return true
	}
	return false
}

type Pricing struct {
	InputPerMTok       float64
	OutputPerMTok      float64
	CachedInputPerMTok float64
}

func (p Pricing) Cost(u chat.Usage) float64 {
	cached := u.CachedPromptTokens
	if cached > u.PromptTokens {
		cached = u.PromptTokens
	}
	uncached := u.PromptTokens - cached
	return (float64(uncached)*p.InputPerMTok +
		float64(cached)*p.CachedInputPerMTok +
		float64(u.CompletionTokens)*p.OutputPerMTok) / 1e6
}

// Target is a provider model the router can select.
type Target struct {
	Name           string
	Provider       string
	Model          string
	Locality       Locality
	Tier           Tier
	ContextWindow  int
	Capabilities   []Capability
	Pricing        Pricing
	AttemptTimeout time.Duration
}

func (t Target) Has(c Capability) bool {
	for _, x := range t.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

func (t Target) IsEmbedding() bool { return t.Has(CapEmbeddings) }

func RequiredCapabilities(r chat.Request) []Capability {
	var caps []Capability
	if len(r.Tools) > 0 {
		caps = append(caps, CapTools)
	}
	if r.HasImage() {
		caps = append(caps, CapVision)
	}
	if len(r.ResponseFormat) > 0 && string(r.ResponseFormat) != "null" {
		caps = append(caps, CapJSONMode)
	}
	return caps
}

type Pool []Target

func (p Pool) ByName(name string) (Target, bool) {
	for _, t := range p {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

func (p Pool) Filter(keep func(Target) bool) Pool {
	out := make(Pool, 0, len(p))
	for _, t := range p {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

type LocalitySet struct {
	Local bool
	Cloud bool
}

func AllLocalities() LocalitySet { return LocalitySet{Local: true, Cloud: true} }

func LocalOnly() LocalitySet { return LocalitySet{Local: true} }

func (s LocalitySet) Allows(l Locality) bool {
	switch l {
	case LocalityLocal:
		return s.Local
	case LocalityCloud:
		return s.Cloud
	}
	return false
}
