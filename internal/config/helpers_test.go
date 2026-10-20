// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/config"
)

func readFile(path string) ([]byte, error) { return os.ReadFile(path) } //nolint:gosec // test fixture path

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "triage.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// validYAML is a minimal valid configuration.
const validYAML = `
auth: { enabled: true, keys_env: TRIAGE_API_KEYS }
providers:
  lm: { type: openaicompat, base_url: "http://localhost:1234/v1" }
  claude: { type: anthropic }
targets:
  - { name: small, provider: lm, model: m, locality: local, tier: weak, context_window: 1000 }
  - { name: big, provider: claude, model: c, locality: cloud, tier: strong, context_window: 1000 }
  - { name: embed, provider: lm, model: e, locality: local, capabilities: [embeddings] }
routes:
  - name: auto
    pool: [small, big]
    strategy: length
    fallback: [tier:strong, big]
`

func validConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(validYAML))
	require.NoError(t, err)
	require.NoError(t, config.Validate(cfg))
	return cfg
}
