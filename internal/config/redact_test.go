// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/config"
)

func TestRedacted_HidesHeaderValuesAndURLCredentials(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Providers["lm"] = config.Provider{Type: "openaicompat", BaseURL: "http://user:pw@localhost:1234/v1"}
	cfg.Targets[0].ExtraHeaders = map[string]string{"X-Key": "secret"}
	r := cfg.Redacted()
	assert.Equal(t, config.RedactedValue, r.Targets[0].ExtraHeaders["X-Key"])
	assert.NotContains(t, r.Providers["lm"].BaseURL, "pw")
	assert.Contains(t, r.Providers["lm"].BaseURL, "localhost:1234")
	assert.Equal(t, "secret", cfg.Targets[0].ExtraHeaders["X-Key"], "the original is untouched")
	r.Routes[0].Pool[0] = "changed"
	assert.NotEqual(t, "changed", cfg.Routes[0].Pool[0], "deep copy")
	require.Equal(t, cfg.Providers["claude"], r.Providers["claude"])
}

func TestValidate_MFEgressAndKey(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Routes[0].Strategy = "mf"
	cfg.Difficulty.MF.Provider = "lm"
	err := config.Validate(cfg)
	require.ErrorContains(t, err, "data egress")
	require.ErrorContains(t, err, "needs api_key_env or api_key_file")
	cfg.Difficulty.MF.AllowExternalEmbedding = true
	p := cfg.Providers["lm"]
	p.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["lm"] = p
	require.NoError(t, config.Validate(cfg))
}
