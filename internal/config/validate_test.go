// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/config"
)

func TestValidateMinimalConfigIsValid(t *testing.T) {
	t.Parallel()
	_ = validConfig(t)
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"server listen malformed", func(c *config.Config) { c.Server.Listen = "8080" }, "server.listen: must be host:port"},
		{"server listen bad port", func(c *config.Config) { c.Server.Listen = ":99999" }, "server.listen: must be host:port"},
		{"admin listen malformed", func(c *config.Config) { c.Admin.Listen = "nope" }, "admin.listen"},
		{"auth without keys", func(c *config.Config) { c.Auth.KeysEnv = "" }, "enabled requires keys_file or keys_env"},
		{"auth both keys", func(c *config.Config) { c.Auth.KeysFile = "/k" }, "mutually exclusive"},
		{"public without auth", func(c *config.Config) {
			c.Auth.Enabled = false
			c.Server.Listen = "0.0.0.0:8080"
		}, "allow_unauthenticated"},
		{"max body zero", func(c *config.Config) { c.API.MaxBodyBytes = 0 }, "api.max_body_bytes"},
		{"no providers", func(c *config.Config) { c.Providers = nil }, "providers: at least one"},
		{"provider type", func(c *config.Config) { c.Providers["lm"] = config.Provider{Type: "x"} }, "providers.lm.type"},
		{"provider base url", func(c *config.Config) {
			c.Providers["lm"] = config.Provider{Type: "openaicompat", BaseURL: "localhost"}
		}, "providers.lm.base_url"},
		{"provider base url unparsable", func(c *config.Config) {
			c.Providers["lm"] = config.Provider{Type: "openaicompat", BaseURL: "http://[::1"}
		}, "providers.lm.base_url"},
		{"provider key exclusive", func(c *config.Config) {
			c.Providers["claude"] = config.Provider{Type: "anthropic", APIKeyEnv: "A", APIKeyFile: "/f"}
		}, "api_key_env and api_key_file"},
		{"provider idle conns", func(c *config.Config) {
			c.Providers["lm"] = config.Provider{
				Type: "openaicompat", BaseURL: "http://x", MaxIdleConnsPerHost: -1,
			}
		}, "max_idle_conns_per_host"},
		{"health probe", func(c *config.Config) { c.Health.Probe = "ping" }, "health.probe"},
		{"health interval", func(c *config.Config) { c.Health.Interval = 0 }, "health.interval"},
		{"health threshold", func(c *config.Config) { c.Health.FailureThreshold = 0 }, "health.failure_threshold"},
		{"health cooldown", func(c *config.Config) { c.Health.Cooldown = 0 }, "health.cooldown"},
		{"selection mode", func(c *config.Config) { c.Selection.Mode = "random" }, "selection.mode"},
		{
			"unknown detector", func(c *config.Config) { c.Privacy.Detectors["emial"] = false },
			"privacy.detectors.emial: unknown detector id; valid ids are credit_card",
		},
		{
			"bert max input low", func(c *config.Config) { c.Difficulty.BERT.MaxInputTokens = 1 },
			"max_input_tokens: must be within [2, 512]",
		},
		{
			"bert max input high", func(c *config.Config) { c.Difficulty.BERT.MaxInputTokens = 513 },
			"max_input_tokens: must be within [2, 512]",
		},
		{"selection tokenizer", func(c *config.Config) { c.Selection.Tokenizer = " " }, "selection.tokenizer"},
		{"no targets", func(c *config.Config) { c.Targets = nil; c.Routes[0].Pool = nil }, "targets: at least one"},
		{"target name missing", func(c *config.Config) { c.Targets[0].Name = "" }, "targets[0].name: is required"},
		{"target name duplicate", func(c *config.Config) { c.Targets[1].Name = "small" }, "duplicate target name"},
		{"target provider", func(c *config.Config) { c.Targets[0].Provider = "zz" }, "unknown provider"},
		{"target model", func(c *config.Config) { c.Targets[0].Model = "" }, ".model: is required"},
		{"target locality", func(c *config.Config) { c.Targets[0].Locality = "edge" }, ".locality"},
		{"target tier", func(c *config.Config) { c.Targets[0].Tier = "" }, ".tier"},
		{"target context window", func(c *config.Config) { c.Targets[0].ContextWindow = 0 }, ".context_window"},
		{"target capability", func(c *config.Config) { c.Targets[0].Capabilities = []string{"magic"} }, ".capabilities"},
		{"embedding with tier", func(c *config.Config) { c.Targets[2].Tier = "weak" }, "must be empty for embedding"},
		{"embedding mixed caps", func(c *config.Config) {
			c.Targets[2].Capabilities = []string{"embeddings", "tools"}
		}, "cannot be combined"},
		{"embedding on anthropic", func(c *config.Config) { c.Targets[2].Provider = "claude" }, "does not support emb"},
		{"embedding negative window", func(c *config.Config) { c.Targets[2].ContextWindow = -1 }, "must not be negative"},
		{"target attempt timeout", func(c *config.Config) { c.Targets[0].AttemptTimeout = -1 }, ".attempt_timeout"},
		{"target pricing", func(c *config.Config) { c.Targets[0].Pricing.OutputPerMTok = -1 }, ".pricing"},
		{"no routes", func(c *config.Config) { c.Routes = nil }, "routes: at least one"},
		{"route name missing", func(c *config.Config) { c.Routes[0].Name = "" }, "routes[0].name: is required"},
		{"route duplicate", func(c *config.Config) { c.Routes = append(c.Routes, c.Routes[0]) }, "duplicate route name"},
		{"route pool unknown", func(c *config.Config) { c.Routes[0].Pool = []string{"nope"} }, ".pool: unknown target"},
		{"route privacy mode", func(c *config.Config) { c.Routes[0].Privacy.Mode = "x" }, "privacy.mode"},
		{"route max client mode", func(c *config.Config) { c.Routes[0].Privacy.MaxClientMode = "x" }, "max_client_mode"},
		{"route max client weaker", func(c *config.Config) {
			c.Routes[0].Privacy.Mode = "local_only"
			c.Routes[0].Privacy.MaxClientMode = "off"
		}, "less strict"},
		{"route strategy", func(c *config.Config) { c.Routes[0].Strategy = "oracle" }, ".strategy"},
		{"route threshold high", func(c *config.Config) { c.Routes[0].Threshold = 1.5 }, ".threshold"},
		{"route threshold negative", func(c *config.Config) { c.Routes[0].Threshold = -0.1 }, ".threshold"},
		{"route default tier", func(c *config.Config) { c.Routes[0].DefaultTier = "mid" }, ".default_tier"},
		{"route tier action", func(c *config.Config) { c.Routes[0].OnTierUnavailable = "panic" }, ".on_tier_unavailable"},
		{"fallback tier", func(c *config.Config) { c.Routes[0].Fallback = []string{"tier:mid"} }, ".fallback"},
		{"fallback unknown target", func(c *config.Config) { c.Routes[0].Fallback = []string{"ghost"} }, "unknown target"},
		{"fallback outside pool", func(c *config.Config) { c.Routes[0].Fallback = []string{"embed"} }, "not in the route"},
		{"match neither", func(c *config.Config) {
			c.Routes[0].Rules.Match = []config.Match{{Tier: "weak"}}
		}, "exactly one of"},
		{"match both", func(c *config.Config) {
			c.Routes[0].Rules.Match = []config.Match{{MinPromptTokens: 1, LastUserRegex: "x", Tier: "weak"}}
		}, "exactly one of"},
		{"match negative tokens", func(c *config.Config) {
			c.Routes[0].Rules.Match = []config.Match{{MinPromptTokens: -5, Tier: "weak"}}
		}, "min_prompt_tokens"},
		{"match bad regex", func(c *config.Config) {
			c.Routes[0].Rules.Match = []config.Match{{LastUserRegex: "(", Tier: "weak"}}
		}, "invalid regular expression"},
		{"match tier", func(c *config.Config) {
			c.Routes[0].Rules.Match = []config.Match{{MinPromptTokens: 5, Tier: "none"}}
		}, "rules.match[0].tier"},
		{"scan bytes", func(c *config.Config) { c.Privacy.MaxScanBytes = 0 }, "privacy.max_scan_bytes"},
		{"oversize", func(c *config.Config) { c.Privacy.OnOversize = "drop" }, "privacy.on_oversize"},
		{"ner score", func(c *config.Config) { c.Privacy.NER.MinScore = 2 }, "privacy.ner.min_score"},
		{"ner tokens", func(c *config.Config) { c.Privacy.NER.MaxTokens = 0 }, "privacy.ner.max_tokens"},
		{"ner timeout", func(c *config.Config) { c.Privacy.NER.Timeout = 0 }, "privacy.ner.timeout"},
		{"difficulty timeout", func(c *config.Config) { c.Difficulty.Timeout = 0 }, "difficulty.timeout"},
		{"difficulty on error", func(c *config.Config) { c.Difficulty.OnError = "x" }, "difficulty.on_error"},
		{"difficulty missing artifact", func(c *config.Config) { c.Difficulty.MissingArtifact = "x" }, "missing_artifact"},
		{"length midpoint", func(c *config.Config) { c.Difficulty.Length.MidpointTokens = 0 }, "length.midpoint_tokens"},
		{"length slope", func(c *config.Config) { c.Difficulty.Length.Slope = 0 }, "length.slope"},
		{"bert variant", func(c *config.Config) { c.Difficulty.BERT.Variant = "fp8" }, "bert.variant"},
		{"bert timeout", func(c *config.Config) { c.Difficulty.BERT.Timeout = 0 }, "bert.timeout"},
		{"bert max input", func(c *config.Config) { c.Difficulty.BERT.MaxInputTokens = 0 }, "bert.max_input_tokens"},
		{"bert threads", func(c *config.Config) { c.Difficulty.BERT.IntraOpThreads = 0 }, "bert.intra_op_threads"},
		{"bert queue", func(c *config.Config) { c.Difficulty.BERT.QueueSize = 0 }, "bert.queue_size"},
		{"bert concurrency", func(c *config.Config) { c.Difficulty.BERT.MaxConcurrency = -1 }, "bert.max_concurrency"},
		{"mf timeout", func(c *config.Config) { c.Difficulty.MF.Timeout = 0 }, "mf.timeout"},
		{"mf fallback", func(c *config.Config) { c.Difficulty.MF.Fallback = "mf" }, "mf.fallback"},
		{"mf cache", func(c *config.Config) { c.Difficulty.MF.CacheSize = -1 }, "mf.cache_size"},
		{"mf provider unknown", func(c *config.Config) { c.Routes[0].Strategy = "mf" }, "mf.provider: unknown"},
		{"mf provider type", func(c *config.Config) {
			c.Routes[0].Strategy = "mf"
			c.Difficulty.MF.Provider = "claude"
		}, "must be of type openaicompat"},
		{"dispatch attempts", func(c *config.Config) { c.Dispatch.MaxAttempts = 0 }, "dispatch.max_attempts"},
		{"dispatch attempt timeout", func(c *config.Config) { c.Dispatch.AttemptTimeout = 0 }, "dispatch.attempt_timeout"},
		{"dispatch total timeout", func(c *config.Config) { c.Dispatch.TotalTimeout = 0 }, "dispatch.total_timeout"},
		{"dispatch total below attempt", func(c *config.Config) {
			c.Dispatch.TotalTimeout = config.Duration(time.Second)
		}, "at least attempt_timeout"},
		{"backoff base", func(c *config.Config) { c.Dispatch.Backoff.Base = 0 }, "backoff.base"},
		{"backoff max", func(c *config.Config) { c.Dispatch.Backoff.Max = 1 }, "backoff.max"},
		{"retry after", func(c *config.Config) { c.Dispatch.MaxRetryAfter = -1 }, "max_retry_after"},
		{"stream failure", func(c *config.Config) { c.Dispatch.StreamFailure = "retry" }, "stream_failure"},
		{"decisions buffer", func(c *config.Config) { c.Decisions.BufferSize = 0 }, "decisions.buffer_size"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			tc.mutate(cfg)
			err := config.Validate(cfg)
			var ve *config.ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidateAcceptsLoopbackWithoutAuth(t *testing.T) {
	t.Parallel()
	for _, listen := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		cfg := validConfig(t)
		cfg.Auth.Enabled = false
		cfg.Server.Listen = listen
		require.NoError(t, config.Validate(cfg), listen)
	}
	cfg := validConfig(t)
	cfg.Auth.Enabled = false
	cfg.Server.Listen = "0.0.0.0:8080"
	cfg.Server.AllowUnauthenticated = true
	require.NoError(t, config.Validate(cfg))
}

func TestValidateCollectsAllErrors(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Server.Listen = "bad"
	cfg.Health.Probe = "x"
	cfg.Targets[0].Provider = "ghost"
	cfg.Routes[0].Threshold = 3
	cfg.Dispatch.MaxAttempts = 0
	cfg.Decisions.BufferSize = -1

	err := config.Validate(cfg)
	var ve *config.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Len(t, ve.Problems, 6)
	for _, want := range []string{
		"server.listen", "health.probe", "unknown provider", "threshold", "max_attempts", "buffer_size",
	} {
		assert.Contains(t, err.Error(), want)
	}
}
