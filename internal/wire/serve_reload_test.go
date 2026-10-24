// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReloadServer(t *testing.T) *server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "triage.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server: { listen: "127.0.0.1:0" }
admin: { listen: "127.0.0.1:0" }
auth: { enabled: false }
models: { cache_dir: `+t.TempDir()+` }
providers: { p: { type: openaicompat, base_url: "http://127.0.0.1:1/v1" } }
health: { interval: 1h }
targets: [{ name: a, provider: p, model: m, locality: local, tier: weak, context_window: 100 }]
routes: [{ name: r, strategy: length }]
`), 0o600))
	s, err := newServer(t.Context(), ServeOptions{ConfigPath: path, WatchInterval: -1})
	require.NoError(t, err)
	require.NotNil(t, s.lv.cur.Load())
	return s
}

func TestServer_ShutdownConcurrentWithReload(t *testing.T) {
	t.Parallel()
	t.Run("reload after shutdown builds and commits nothing", func(t *testing.T) {
		t.Parallel()
		s := newReloadServer(t)
		s.closeAll(t.Context())
		require.ErrorIs(t, s.mgr.Reload(), errServerClosed)
		assert.Nil(t, s.lv.cur.Load(), "no generation committed after shutdown")
		assert.Nil(t, s.pending)
	})
	t.Run("concurrent", func(t *testing.T) {
		t.Parallel()
		for range 20 {
			s := newReloadServer(t)
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Go(func() {
				<-start
				for range 3 {
					_ = s.mgr.Reload()
				}
			})
			wg.Go(func() {
				<-start
				s.closeAll(t.Context())
			})
			close(start)
			wg.Wait()
			s.mu.Lock()
			pending, closed := s.pending, s.closed
			s.mu.Unlock()
			assert.Nil(t, s.lv.cur.Load(), "no generation live after shutdown")
			assert.Nil(t, pending, "no generation pending after shutdown")
			assert.True(t, closed)
		}
	})
}
