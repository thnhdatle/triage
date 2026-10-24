// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package wire_test

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pinned "github.com/thnhdatle/triage/models"
)

func bertCache(t *testing.T, withINT8 bool) string {
	t.Helper()
	dir := os.Getenv("TRIAGE_TOKENIZER_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		dir = filepath.Join(home, ".cache", "rayld", "triage", "tokenizers")
	}
	tok := filepath.Join(dir, "bert", "tokenizer.json")
	if _, err := os.Stat(tok); errors.Is(err, fs.ErrNotExist) {
		require.FailNowf(t, "tokenizer file missing", "%s not found: run `make bootstrap`", tok)
	}
	m, err := pinned.Manifest()
	require.NoError(t, err)
	e, _ := m.Entry("bert")
	cache := t.TempDir()
	dst := filepath.Join(cache, e.Name, e.Revision)
	require.NoError(t, os.MkdirAll(dst, 0o750))
	require.NoError(t, os.Symlink(tok, filepath.Join(dst, "tokenizer.json")))
	files := []string{"graph.onnx", "model.safetensors"}
	if withINT8 {
		files = append(files, "model.int8.onnx")
	}
	for _, name := range files {
		b, err := os.ReadFile(filepath.Join("../../testdata/models/bert-tiny", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, name), b, 0o600))
	}
	return cache
}

func TestServe_BERT_ScoresAndReusesSessionOnReload(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	cache := bertCache(t, true)
	routes := `
models: { cache_dir: ` + cache + ` }
selection: { tokenizer: bert }
difficulty: { bert: { intra_op_threads: 1, max_concurrency: 2 } }
routes:
  - { name: learned, pool: [local-small, cloud-strong], privacy: { mode: off }, strategy: bert, threshold: 0.5 }
`
	tr := startTriage(t, baseConfig(f, routes))
	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("learned", "prove the theorem", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	full := tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions/"+r.header.Get("X-Triage-Decision"), nil)
	assert.Contains(t, string(full.body), `"strategy":"bert"`)
	assert.Contains(t, string(full.body), `"variant":"int8"`)
	assert.NotContains(t, string(full.body), "error->", "scored without on_error")

	cfg := baseConfig(f, strings.Replace(routes, "threshold: 0.5", "threshold: 0.7", 1))
	require.NoError(t, os.WriteFile(tr.cfgPath, []byte(cfg), 0o600))
	require.Equal(t, http.StatusOK, tr.call(t, http.MethodPost, tr.admin+"/v1/admin/reload", nil).status)
	cfg = strings.Replace(cfg, "intra_op_threads: 1,", "intra_op_threads: 1, variant: fp32,", 1)
	require.NoError(t, os.WriteFile(tr.cfgPath, []byte(cfg), 0o600))
	require.Equal(t, http.StatusOK, tr.call(t, http.MethodPost, tr.admin+"/v1/admin/reload", nil).status)
	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("learned", "prove it", true))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	full = tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions/"+r.header.Get("X-Triage-Decision"), nil)
	assert.Contains(t, string(full.body), `"variant":"fp32"`)
	assert.Contains(t, tr.metrics(t), `triage_scoring_duration_seconds_count{strategy="bert"} 2`)
}
