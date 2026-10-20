// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Enumerations of option values.
var (
	providerTypes  = []string{typeOpenAICompat, "anthropic"}
	probeKinds     = []string{"models", "completion"}
	selectionModes = []string{"priority", "round_robin", "least_inflight"}
	localities     = []string{"local", "cloud"}
	tiers          = []string{"weak", "strong"}
	capabilities   = []string{"tools", "vision", "json_mode", "embeddings"}
	privacyModes   = []string{"off", "mask", valLocalOnly}
	oversizeModes  = []string{valLocalOnly, "reject"}
	strategies     = []string{"none", "random", "length", valBERT, "mf"}
	tierActions    = []string{"escalate", "degrade", "fail"}
	missingModes   = []string{valError, "fallback"}
	bertVariants   = []string{"auto", "int8", "fp32"}
	mfFallbacks    = []string{valBERT, "length"}
	streamFailures = []string{"continue", "error"}
	// detectorIDs mirrors piiregex.IDs (a wire test keeps them equal).
	detectorIDs = []string{
		"credit_card", "iban", "email", "ip", "us_ssn", "secret", "kr_rrn", "phone", "vn_cccd", "street_address",
	}
)

type problems struct{ list []string }

func (p *problems) add(path, format string, args ...any) {
	p.list = append(p.list, path+": "+fmt.Sprintf(format, args...))
}

func (p *problems) oneOf(path, v string, allowed []string) {
	if !slices.Contains(allowed, v) {
		p.add(path, "must be one of %s, got %q", strings.Join(allowed, " | "), v)
	}
}

func (p *problems) positive(path string, ok bool) {
	if !ok {
		p.add(path, "must be greater than 0")
	}
}

// Validate checks cfg and returns a *ValidationError listing every problem, or
// nil. It checks values and cross references only; it does not touch the
// filesystem or the network.
func Validate(cfg *Config) error {
	p := &problems{}
	p.server(cfg)
	p.providers(cfg)
	p.health(cfg)
	p.selection(cfg)
	p.targets(cfg)
	p.routes(cfg)
	p.privacy(cfg)
	p.difficulty(cfg)
	p.dispatch(cfg)
	p.positive("decisions.buffer_size", cfg.Decisions.BufferSize > 0)
	if len(p.list) == 0 {
		return nil
	}
	return &ValidationError{Problems: p.list}
}

func validListen(addr string) (host string, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(port)
	return host, err == nil && n >= 0 && n <= 65535
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *problems) server(cfg *Config) {
	host, ok := validListen(cfg.Server.Listen)
	if !ok {
		p.add("server.listen", "must be host:port, got %q", cfg.Server.Listen)
	}
	if _, ok := validListen(cfg.Admin.Listen); !ok {
		p.add("admin.listen", "must be host:port, got %q", cfg.Admin.Listen)
	}
	if cfg.Auth.Enabled {
		if cfg.Auth.KeysFile == "" && cfg.Auth.KeysEnv == "" {
			p.add("auth", "enabled requires keys_file or keys_env")
		}
		if cfg.Auth.KeysFile != "" && cfg.Auth.KeysEnv != "" {
			p.add("auth", "keys_file and keys_env are mutually exclusive")
		}
	} else if ok && !isLoopback(host) && !cfg.Server.AllowUnauthenticated {
		p.add("server.listen", "non-loopback address %q without auth requires server.allow_unauthenticated: true",
			cfg.Server.Listen)
	}
	p.positive("api.max_body_bytes", cfg.API.MaxBodyBytes > 0)
}

func (p *problems) providers(cfg *Config) {
	if len(cfg.Providers) == 0 {
		p.add("providers", "at least one provider is required")
	}
	for name, pr := range cfg.Providers {
		path := "providers." + name
		p.oneOf(path+".type", pr.Type, providerTypes)
		if pr.Type == typeOpenAICompat {
			u, err := url.Parse(pr.BaseURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				p.add(path+".base_url", "must be an http(s) URL, got %q", pr.BaseURL)
			}
		}
		if pr.APIKeyEnv != "" && pr.APIKeyFile != "" {
			p.add(path, "api_key_env and api_key_file are mutually exclusive")
		}
		if pr.MaxIdleConnsPerHost < 0 {
			p.add(path+".max_idle_conns_per_host", "must not be negative")
		}
	}
}

func (p *problems) health(cfg *Config) {
	h := cfg.Health
	p.oneOf("health.probe", h.Probe, probeKinds)
	p.positive("health.interval", h.Interval > 0)
	p.positive("health.failure_threshold", h.FailureThreshold > 0)
	p.positive("health.cooldown", h.Cooldown > 0)
}

func (p *problems) selection(cfg *Config) {
	p.oneOf("selection.mode", cfg.Selection.Mode, selectionModes)
	if strings.TrimSpace(cfg.Selection.Tokenizer) == "" {
		p.add("selection.tokenizer", "must be estimate, bert or a path to a tokenizer.json")
	}
}

func (p *problems) targets(cfg *Config) {
	if len(cfg.Targets) == 0 {
		p.add("targets", "at least one target is required")
	}
	seen := map[string]bool{}
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		path := fmt.Sprintf("targets[%d]", i)
		if t.Name == "" {
			p.add(path+".name", "is required")
		} else {
			if seen[t.Name] {
				p.add(path+".name", "duplicate target name %q", t.Name)
			}
			seen[t.Name] = true
			path = fmt.Sprintf("targets[%d] (%s)", i, t.Name)
		}
		p.target(cfg, t, path)
	}
}

func (p *problems) target(cfg *Config, t *Target, path string) {
	pr, ok := cfg.Providers[t.Provider]
	if !ok {
		p.add(path+".provider", "unknown provider %q", t.Provider)
	}
	if t.Model == "" {
		p.add(path+".model", "is required")
	}
	p.oneOf(path+".locality", t.Locality, localities)
	embedding := false
	for _, c := range t.Capabilities {
		p.oneOf(path+".capabilities", c, capabilities)
		embedding = embedding || c == "embeddings"
	}
	switch {
	case embedding:
		if len(t.Capabilities) > 1 {
			p.add(path+".capabilities", "embeddings cannot be combined with other capabilities")
		}
		if t.Tier != "" {
			p.add(path+".tier", "must be empty for embedding targets, got %q", t.Tier)
		}
		if ok && pr.Type == "anthropic" {
			p.add(path+".capabilities", "provider %q (anthropic) does not support embeddings", t.Provider)
		}
		if t.ContextWindow < 0 {
			p.add(path+".context_window", "must not be negative")
		}
	default:
		p.oneOf(path+".tier", t.Tier, tiers)
		p.positive(path+".context_window", t.ContextWindow > 0)
	}
	if t.AttemptTimeout < 0 {
		p.add(path+".attempt_timeout", "must not be negative")
	}
	if t.Pricing.InputPerMTok < 0 || t.Pricing.OutputPerMTok < 0 || t.Pricing.CachedInputPerMTok < 0 {
		p.add(path+".pricing", "prices must not be negative")
	}
}

func (p *problems) routes(cfg *Config) {
	if len(cfg.Routes) == 0 {
		p.add("routes", "at least one route is required")
	}
	targets := map[string]*Target{}
	for i := range cfg.Targets {
		targets[cfg.Targets[i].Name] = &cfg.Targets[i]
	}
	seen := map[string]bool{}
	for i := range cfg.Routes {
		r := &cfg.Routes[i]
		path := fmt.Sprintf("routes[%d]", i)
		if r.Name == "" {
			p.add(path+".name", "is required")
		} else {
			if seen[r.Name] {
				p.add(path+".name", "duplicate route name %q", r.Name)
			}
			seen[r.Name] = true
			path = fmt.Sprintf("routes[%d] (%s)", i, r.Name)
		}
		p.route(r, targets, path)
	}
}

func (p *problems) route(r *Route, targets map[string]*Target, path string) {
	pool := map[string]bool{}
	for _, name := range r.Pool {
		if _, ok := targets[name]; !ok {
			p.add(path+".pool", "unknown target %q", name)
		}
		pool[name] = true
	}
	p.routePrivacy(r, path)
	p.oneOf(path+".strategy", r.Strategy, strategies)
	if r.Threshold < 0 || r.Threshold > 1 {
		p.add(path+".threshold", "must be within [0, 1], got %v", r.Threshold)
	}
	p.oneOf(path+".default_tier", r.DefaultTier, tiers)
	p.oneOf(path+".on_tier_unavailable", r.OnTierUnavailable, tierActions)
	for _, f := range r.Fallback {
		if after, ok := strings.CutPrefix(f, "tier:"); ok {
			p.oneOf(path+".fallback", after, tiers)
			continue
		}
		if _, ok := targets[f]; !ok {
			p.add(path+".fallback", "unknown target %q (use a target name or tier:weak|tier:strong)", f)
		} else if len(pool) > 0 && !pool[f] {
			p.add(path+".fallback", "target %q is not in the route pool", f)
		}
	}
	for i, m := range r.Rules.Match {
		p.match(m, fmt.Sprintf("%s.rules.match[%d]", path, i))
	}
}

func (p *problems) routePrivacy(r *Route, path string) {
	p.oneOf(path+".privacy.mode", r.Privacy.Mode, privacyModes)
	p.oneOf(path+".privacy.max_client_mode", r.Privacy.MaxClientMode, privacyModes)
	rank := func(m string) int { return slices.Index(privacyModes, m) }
	if rank(r.Privacy.Mode) >= 0 && rank(r.Privacy.MaxClientMode) >= 0 &&
		rank(r.Privacy.MaxClientMode) < rank(r.Privacy.Mode) {
		p.add(path+".privacy.max_client_mode", "%q is less strict than mode %q", r.Privacy.MaxClientMode, r.Privacy.Mode)
	}
}

func (p *problems) match(m Match, path string) {
	hasTokens, hasRegex := m.MinPromptTokens != 0, m.LastUserRegex != ""
	switch {
	case hasTokens == hasRegex:
		p.add(path, "set exactly one of min_prompt_tokens or last_user_regex")
	case hasTokens && m.MinPromptTokens < 0:
		p.add(path+".min_prompt_tokens", "must be greater than 0")
	case hasRegex:
		if _, err := regexp.Compile(m.LastUserRegex); err != nil {
			p.add(path+".last_user_regex", "invalid regular expression: %v", err)
		}
	}
	p.oneOf(path+".tier", m.Tier, tiers)
}

func (p *problems) privacy(cfg *Config) {
	pv := cfg.Privacy
	p.positive("privacy.max_scan_bytes", pv.MaxScanBytes > 0)
	p.oneOf("privacy.on_oversize", pv.OnOversize, oversizeModes)
	for _, id := range slices.Sorted(maps.Keys(pv.Detectors)) {
		if !slices.Contains(detectorIDs, id) {
			p.add("privacy.detectors."+id, "unknown detector id; valid ids are %s", strings.Join(detectorIDs, ", "))
		}
	}
	if pv.NER.MinScore < 0 || pv.NER.MinScore > 1 {
		p.add("privacy.ner.min_score", "must be within [0, 1], got %v", pv.NER.MinScore)
	}
	p.positive("privacy.ner.max_tokens", pv.NER.MaxTokens > 0)
	p.positive("privacy.ner.timeout", pv.NER.Timeout > 0)
}

func (p *problems) difficulty(cfg *Config) {
	d := cfg.Difficulty
	p.positive("difficulty.timeout", d.Timeout > 0)
	p.oneOf("difficulty.on_error", d.OnError, tiers)
	p.oneOf("difficulty.missing_artifact", d.MissingArtifact, missingModes)
	p.positive("difficulty.length.midpoint_tokens", d.Length.MidpointTokens > 0)
	p.positive("difficulty.length.slope", d.Length.Slope > 0)

	b := d.BERT
	p.oneOf("difficulty.bert.variant", b.Variant, bertVariants)
	p.positive("difficulty.bert.timeout", b.Timeout > 0)
	if b.MaxInputTokens < 2 || b.MaxInputTokens > 512 {
		p.add("difficulty.bert.max_input_tokens", "must be within [2, 512], got %d", b.MaxInputTokens)
	}
	p.positive("difficulty.bert.intra_op_threads", b.IntraOpThreads > 0)
	p.positive("difficulty.bert.queue_size", b.QueueSize > 0)
	if b.MaxConcurrency < 0 {
		p.add("difficulty.bert.max_concurrency", "must not be negative (0 = automatic)")
	}

	m := d.MF
	p.positive("difficulty.mf.timeout", m.Timeout > 0)
	p.oneOf("difficulty.mf.fallback", m.Fallback, mfFallbacks)
	if m.CacheSize < 0 {
		p.add("difficulty.mf.cache_size", "must not be negative")
	}
	if usesStrategy(cfg, "mf") {
		if !m.AllowExternalEmbedding {
			p.add("difficulty.mf.allow_external_embedding", "a route uses strategy mf, which sends the scored "+
				"text of zero-finding requests to an external embedding API (data egress); "+
				"set it to true to allow this")
		}
		pr, ok := cfg.Providers[m.Provider]
		switch {
		case !ok:
			p.add("difficulty.mf.provider", "unknown provider %q (a route uses strategy mf)", m.Provider)
		case pr.Type != typeOpenAICompat:
			p.add("difficulty.mf.provider", "provider %q must be of type openaicompat to embed", m.Provider)
		case pr.APIKeyEnv == "" && pr.APIKeyFile == "":
			p.add("difficulty.mf.provider", "provider %q needs api_key_env or api_key_file for mf embeddings",
				m.Provider)
		}
	}
}

func usesStrategy(cfg *Config, name string) bool {
	return slices.ContainsFunc(cfg.Routes, func(r Route) bool { return r.Strategy == name })
}

func (p *problems) dispatch(cfg *Config) {
	d := cfg.Dispatch
	p.positive("dispatch.max_attempts", d.MaxAttempts > 0)
	p.positive("dispatch.attempt_timeout", d.AttemptTimeout > 0)
	p.positive("dispatch.total_timeout", d.TotalTimeout > 0)
	if d.TotalTimeout > 0 && d.AttemptTimeout > d.TotalTimeout {
		p.add("dispatch.total_timeout", "must be at least attempt_timeout")
	}
	p.positive("dispatch.backoff.base", d.Backoff.Base > 0)
	if d.Backoff.Max < d.Backoff.Base {
		p.add("dispatch.backoff.max", "must be at least backoff.base")
	}
	if d.MaxRetryAfter < 0 {
		p.add("dispatch.max_retry_after", "must not be negative")
	}
	p.oneOf("dispatch.stream_failure", d.StreamFailure, streamFailures)
}
