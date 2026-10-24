// SPDX-License-Identifier: Apache-2.0

package config

import (
	"maps"
	"net/url"
	"slices"
)

const RedactedValue = "[redacted]"

func (c *Config) Redacted() *Config {
	out := *c
	out.Providers = make(map[string]Provider, len(c.Providers))
	for name, p := range c.Providers {
		p.BaseURL = redactURL(p.BaseURL)
		out.Providers[name] = p
	}
	out.Targets = slices.Clone(c.Targets)
	for i := range out.Targets {
		t := &out.Targets[i]
		t.Capabilities = slices.Clone(t.Capabilities)
		if len(t.ExtraHeaders) > 0 {
			h := make(map[string]string, len(t.ExtraHeaders))
			for k := range t.ExtraHeaders {
				h[k] = RedactedValue
			}
			t.ExtraHeaders = h
		}
	}
	out.Routes = slices.Clone(c.Routes)
	for i := range out.Routes {
		r := &out.Routes[i]
		r.Pool = slices.Clone(r.Pool)
		r.Fallback = slices.Clone(r.Fallback)
		r.Rules.Match = slices.Clone(r.Rules.Match)
	}
	out.Privacy.Detectors = maps.Clone(c.Privacy.Detectors)
	return &out
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User(RedactedValue)
	return u.String()
}
