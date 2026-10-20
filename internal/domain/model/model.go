// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

// Locality says whether a target runs on infrastructure the operator controls.
type Locality string

// Localities.
const (
	LocalityLocal Locality = "local"
	LocalityCloud Locality = "cloud"
)

// Valid reports whether l is a known locality.
func (l Locality) Valid() bool { return l == LocalityLocal || l == LocalityCloud }

// Tier is the quality/cost class of a chat target.
type Tier string

// Tiers. TierNone is used by embedding targets.
const (
	TierNone   Tier = ""
	TierWeak   Tier = "weak"
	TierStrong Tier = "strong"
)

// Valid reports whether t is weak or strong.
func (t Tier) Valid() bool { return t == TierWeak || t == TierStrong }

// Other returns the opposite chat tier (weak<->strong); TierNone stays TierNone.
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

// Capability is a feature a target supports.
type Capability string

// Capabilities.
const (
	CapTools      Capability = "tools"
	CapVision     Capability = "vision"
	CapJSONMode   Capability = "json_mode"
	CapEmbeddings Capability = "embeddings"
)

// Valid reports whether c is a known capability.
func (c Capability) Valid() bool {
	switch c {
	case CapTools, CapVision, CapJSONMode, CapEmbeddings:
		return true
	}
	return false
}

// Pricing is USD per one million tokens.
type Pricing struct {
	InputPerMTok       float64
	OutputPerMTok      float64
	CachedInputPerMTok float64
}

// Cost returns the USD cost of a usage. Cached prompt tokens are part of the
// prompt tokens and are billed at the cached rate.
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

// Target is a provider + model name + routing attributes.
type Target struct {
	Name string
	// Provider is the name of the provider config the target uses.
	Provider string
	// Model is the upstream model name sent on each attempt.
	Model         string
	Locality      Locality
	Tier          Tier
	ContextWindow int
	Capabilities  []Capability
	Pricing       Pricing
	// AttemptTimeout overrides dispatch.attempt_timeout when > 0.
	AttemptTimeout time.Duration
}

// Has reports whether the target has the capability.
func (t Target) Has(c Capability) bool {
	for _, x := range t.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// IsEmbedding reports whether the target serves embeddings only.
func (t Target) IsEmbedding() bool { return t.Has(CapEmbeddings) }

// RequiredCapabilities returns the capabilities a chat request needs:
// tools when it has tools, vision when it has images, json_mode when
// response_format is set.
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

// Pool is an ordered list of targets; order is the priority order.
type Pool []Target

// ByName returns the target with the name.
func (p Pool) ByName(name string) (Target, bool) {
	for _, t := range p {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// Filter returns the targets for which keep returns true, preserving order.
func (p Pool) Filter(keep func(Target) bool) Pool {
	out := make(Pool, 0, len(p))
	for _, t := range p {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

// LocalitySet is the set of localities a request may be sent to.
type LocalitySet struct {
	Local bool
	Cloud bool
}

// AllLocalities returns the set that allows local and cloud targets.
func AllLocalities() LocalitySet { return LocalitySet{Local: true, Cloud: true} }

// LocalOnly returns the set that allows local targets only.
func LocalOnly() LocalitySet { return LocalitySet{Local: true} }

// Allows reports whether the locality is in the set.
func (s LocalitySet) Allows(l Locality) bool {
	switch l {
	case LocalityLocal:
		return s.Local
	case LocalityCloud:
		return s.Cloud
	}
	return false
}
