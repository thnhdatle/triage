// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/config"
)

func rewrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func atomicWrite(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o600))
	require.NoError(t, os.Rename(tmp, path))
}

func withAttempts(n string) string { return validYAML + "dispatch: { max_attempts: " + n + " }\n" }

func TestNewManagerErrors(t *testing.T) {
	t.Parallel()
	_, err := config.NewManager(t.TempDir() + "/missing.yaml")
	require.ErrorContains(t, err, "read")
	_, err = config.NewManager(writeConfig(t, "routes: []\n"))
	var ve *config.ValidationError
	require.ErrorAs(t, err, &ve)
}

func TestReloadSwapsAndKeepsOldOnError(t *testing.T) {
	t.Parallel()
	var events []config.ReloadEvent
	path := writeConfig(t, withAttempts("3"))
	m, err := config.NewManager(path, config.WithOnReload(func(e config.ReloadEvent) { events = append(events, e) }))
	require.NoError(t, err)
	assert.Equal(t, path, m.Path())
	first := m.Current()
	assert.Equal(t, 3, first.Dispatch.MaxAttempts)

	rewrite(t, path, withAttempts("5"))
	require.NoError(t, m.Reload())
	second := m.Current()
	assert.Equal(t, 5, second.Dispatch.MaxAttempts)
	assert.Equal(t, 3, first.Dispatch.MaxAttempts, "old config is untouched for in-flight requests")
	require.Len(t, events, 1)
	assert.Same(t, first, events[0].Old)
	assert.Same(t, second, events[0].New)
	require.NoError(t, events[0].Err)

	rewrite(t, path, withAttempts("0"))
	require.Error(t, m.Reload())
	assert.Same(t, second, m.Current(), "invalid config is rejected")
	require.Len(t, events, 2)
	assert.Nil(t, events[1].New)
	assert.Same(t, second, events[1].Old)
	require.Error(t, events[1].Err)

	rewrite(t, path, validYAML+"bogus: 1\n")
	require.Error(t, m.Reload(), "unknown key")
	assert.Same(t, second, m.Current())

	require.NoError(t, os.Remove(path))
	require.ErrorContains(t, m.Reload(), "read")
	assert.Same(t, second, m.Current())
	require.Len(t, events, 4)
}

func TestReloadWithoutCallback(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, validYAML)
	m, err := config.NewManager(path)
	require.NoError(t, err)
	require.NoError(t, m.Reload())
	require.NoError(t, os.Remove(path))
	require.Error(t, m.Reload())
}

func TestRunReloadsOnChange(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, withAttempts("3"))
	var ok, bad atomic.Int32
	m, err := config.NewManager(path,
		config.WithWatchInterval(5*time.Millisecond),
		config.WithWatchInterval(0),
		config.WithOnReload(func(e config.ReloadEvent) {
			if e.Err != nil {
				bad.Add(1)
			} else {
				ok.Add(1)
			}
		}))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	eventually := func(cond func() bool) {
		t.Helper()
		require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond)
	}

	time.Sleep(30 * time.Millisecond)
	assert.Zero(t, ok.Load()+bad.Load(), "unchanged file is not reloaded")

	atomicWrite(t, path, withAttempts("7"))
	eventually(func() bool { return m.Current().Dispatch.MaxAttempts == 7 })

	atomicWrite(t, path, withAttempts("0"))
	eventually(func() bool { return bad.Load() == 1 })
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, int32(1), bad.Load(), "an invalid file is reported once")
	assert.Equal(t, 7, m.Current().Dispatch.MaxAttempts)

	require.NoError(t, os.Remove(path))
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, 7, m.Current().Dispatch.MaxAttempts, "a vanished file is ignored")

	atomicWrite(t, path, withAttempts("9"))
	eventually(func() bool { return m.Current().Dispatch.MaxAttempts == 9 })

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestReloadWhileReading(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, withAttempts("3"))
	m, err := config.NewManager(path)
	require.NoError(t, err)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				cfg := m.Current()
				if n := cfg.Dispatch.MaxAttempts; n != 3 && n != 4 {
					assert.Fail(t, "unexpected attempts", n)
				}
			}
		}()
	}
	var reloaders sync.WaitGroup
	for r := range 4 {
		reloaders.Add(1)
		go func() {
			defer reloaders.Done()
			for range 100 {
				assert.NoError(t, m.Reload(), "reloader %d", r)
			}
		}()
	}
	for i := range 50 {
		n := "3"
		if i%2 == 1 {
			n = "4"
		}
		atomicWrite(t, path, withAttempts(n))
	}
	reloaders.Wait()
	close(stop)
	readers.Wait()
}

func TestWithCheck_RejectsLikeValidation(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, withAttempts("3"))
	reject := atomic.Bool{}
	var events []config.ReloadEvent
	m, err := config.NewManager(path,
		config.WithCheck(func(c *config.Config) error {
			if reject.Load() {
				return errors.New("artifact missing")
			}
			return nil
		}),
		config.WithOnReload(func(e config.ReloadEvent) { events = append(events, e) }))
	require.NoError(t, err)
	first := m.Current()

	reject.Store(true)
	rewrite(t, path, withAttempts("4"))
	require.ErrorContains(t, m.Reload(), "artifact missing")
	assert.Same(t, first, m.Current())
	require.Len(t, events, 1)
	require.Error(t, events[0].Err)

	_, err = config.NewManager(path, config.WithCheck(func(*config.Config) error { return errors.New("no") }))
	require.ErrorContains(t, err, "no")
}
