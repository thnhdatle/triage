// SPDX-License-Identifier: Apache-2.0

// Package model defines model targets: Tier, Locality, Capability, Pricing and
// Pool. A Target is a provider plus a model name plus the attributes the
// router selects on.
//
//	t := model.Target{Name: "local-small", Locality: model.LocalityLocal, Tier: model.TierWeak}
//	cost := t.Pricing.Cost(usage)
package model
