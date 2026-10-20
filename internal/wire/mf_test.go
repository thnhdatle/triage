// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/domain/artifact"
	pinned "github.com/thnhdatle/triage/models"
)

// mfCache lays out a models.cache_dir holding the mf vector derived from the
// mf-tiny fixture (random weights with the real shapes).
func mfCache(t *testing.T) string {
	t.Helper()
	m, err := pinned.Manifest()
	require.NoError(t, err)
	e, ok := m.Entry("mf")
	require.True(t, ok)
	cache := t.TempDir()
	dir := filepath.Join(cache, e.Name, e.Revision)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	w, err := os.ReadFile("../../testdata/models/mf-tiny/model.safetensors")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, mf.WeightsFileName), w, 0o600))
	d, err := mf.Deriver{}.Derive(t.Context(), artifact.Entry{}, dir)
	require.NoError(t, err)
	require.FileExists(t, d.Path)
	return cache
}

func TestServe_MF_EgressGatingCacheAndCost(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	cache := mfCache(t)
	cfg := strings.Replace(baseConfig(f, `
models: { cache_dir: `+cache+` }
difficulty: { mf: { allow_external_embedding: true, provider: fake } }
routes:
  - { name: mf-route, pool: [local-small, cloud-strong], privacy: { mode: off }, strategy: mf, threshold: 0.5 }
`), "  fake: { type: openaicompat, base_url:", "  fake: { type: openaicompat, api_key_env: FAKE_KEY, base_url:", 1)
	cfg = strings.Replace(cfg, "  - { name: local-embed,",
		"  - { name: cloud-embed, provider: fake, model: text-embedding-3-small, locality: cloud,\n"+
			"      capabilities: [embeddings], pricing: { input_per_mtok: 0.02 } }\n  - { name: local-embed,", 1)
	tr := startTriage(t, cfg)
	embeds := func() int {
		n := 0
		for _, r := range f.requests(t) {
			if r.Kind == "embeddings" {
				n++
				assert.Equal(t, "Bearer sk-test", r.Headers.Get("Authorization"))
				assert.NotContains(t, r.Body, "@example.com", "no PII leaves the machine")
			}
		}
		return n
	}

	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("mf-route", "explain monads", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	d := tr.decision(t, r.header.Get("X-Triage-Decision"))
	assert.Equal(t, "mf", d.Strategy)
	assert.Equal(t, 1, embeds())
	full := tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions/"+d.ID, nil)
	assert.Contains(t, string(full.body), `"scoring_cost_usd":`)
	assert.NotContains(t, string(full.body), `"scoring_cost_usd":0,`, "mf embedding cost is priced (F9)")

	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("mf-route", "explain monads", false))
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, 1, embeds(), "the second identical prompt is a cache hit")

	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions",
		chatBody("mf-route", "write to jo@example.com", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	d = tr.decision(t, r.header.Get("X-Triage-Decision"))
	assert.Contains(t, d.Reasons, "strategy:mf->length(privacy)")
	assert.Equal(t, 1, embeds(), "a request with findings never reaches the embedding API")

	m := tr.metrics(t)
	assert.Contains(t, m, `triage_mf_external_embeddings_total{route="mf-route"} 1`)
	assert.Contains(t, m, `triage_cost_usd_total{route="mf-route",target="cloud-embed"}`)
	findSpan(t, tr.exporter.GetSpans(), "embeddings text-embedding-3-small")
}
