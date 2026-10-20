// SPDX-License-Identifier: Apache-2.0

package embedcache_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/embedcache"
)

// fakeEmbedder returns [len(text), dimensions] and records every call.
type fakeEmbedder struct {
	mu    sync.Mutex
	calls [][]string
	err   error
	short bool
}

func (f *fakeEmbedder) Embed(_ context.Context, _ string, dimensions int, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), texts...))
	if f.err != nil {
		return nil, f.err
	}
	if f.short {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len(t)), float32(dimensions)}
	}
	return out, nil
}

func (f *fakeEmbedder) callLog() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

func newCache(t *testing.T, f *fakeEmbedder, size int) *embedcache.Cache {
	t.Helper()
	c, err := embedcache.New(f, size)
	require.NoError(t, err)
	return c
}

func TestCache_New_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		next *fakeEmbedder
		size int
	}{
		{name: "zero size", next: &fakeEmbedder{}, size: 0},
		{name: "negative size", next: &fakeEmbedder{}, size: -3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := embedcache.New(tc.next, tc.size)
			require.Error(t, err)
		})
	}
	_, err := embedcache.New(nil, 1)
	require.Error(t, err)
}

func TestCache_Embed_HitAndMiss(t *testing.T) {
	t.Parallel()
	f := &fakeEmbedder{}
	c := newCache(t, f, 10)
	ctx := t.Context()

	v1, err := c.Embed(ctx, "m", 2, []string{"ab"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{2, 2}}, v1)
	v2, err := c.Embed(ctx, "m", 2, []string{"ab"})
	require.NoError(t, err)
	assert.Equal(t, v1, v2)

	assert.Len(t, f.callLog(), 1)
	assert.Equal(t, embedcache.Stats{Hits: 1, Misses: 1}, c.Stats())
	assert.Equal(t, 1, c.Len())

	out, err := c.Embed(ctx, "m", 2, nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestCache_Embed_KeyIncludesModelAndDimensions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		model string
		dims  int
	}{
		{name: "other model", model: "m2", dims: 2},
		{name: "other dimensions", model: "m", dims: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeEmbedder{}
			c := newCache(t, f, 10)
			_, err := c.Embed(t.Context(), "m", 2, []string{"x"})
			require.NoError(t, err)
			_, err = c.Embed(t.Context(), tc.model, tc.dims, []string{"x"})
			require.NoError(t, err)
			assert.Len(t, f.callLog(), 2)
			assert.Equal(t, uint64(0), c.Stats().Hits)
		})
	}
}

func TestCache_Embed_PartialBatchPreservesOrder(t *testing.T) {
	t.Parallel()
	f := &fakeEmbedder{}
	c := newCache(t, f, 10)
	ctx := t.Context()
	_, err := c.Embed(ctx, "m", 2, []string{"bb"})
	require.NoError(t, err)

	got, err := c.Embed(ctx, "m", 2, []string{"a", "bb", "ccc", "dddd"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 2}, {2, 2}, {3, 2}, {4, 2}}, got)
	calls := f.callLog()
	require.Len(t, calls, 2)
	assert.Equal(t, []string{"a", "ccc", "dddd"}, calls[1])
	assert.Equal(t, embedcache.Stats{Hits: 1, Misses: 4}, c.Stats())
}

func TestCache_Embed_EvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()
	f := &fakeEmbedder{}
	c := newCache(t, f, 2)
	ctx := t.Context()
	embed := func(s string) {
		t.Helper()
		_, err := c.Embed(ctx, "m", 2, []string{s})
		require.NoError(t, err)
	}
	embed("a")
	embed("bb")
	embed("a")   // a is now most recent
	embed("ccc") // evicts bb
	assert.Equal(t, 2, c.Len())

	before := len(f.callLog())
	embed("a") // still cached
	assert.Len(t, f.callLog(), before)
	embed("bb") // evicted, refetched
	assert.Len(t, f.callLog(), before+1)
}

func TestCache_Embed_ErrorsAreNotCached(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name string
		f    *fakeEmbedder
		want error
	}{
		{name: "upstream error", f: &fakeEmbedder{err: boom}, want: boom},
		{name: "wrong vector count", f: &fakeEmbedder{short: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newCache(t, tc.f, 4)
			_, err := c.Embed(t.Context(), "m", 2, []string{"a"})
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			assert.Equal(t, 0, c.Len())
		})
	}
}

func TestCache_Embed_ReturnsCopies(t *testing.T) {
	t.Parallel()
	f := &fakeEmbedder{}
	c := newCache(t, f, 4)
	first, err := c.Embed(t.Context(), "m", 2, []string{"ab"})
	require.NoError(t, err)
	first[0][0] = 99 // mutate the miss result

	second, err := c.Embed(t.Context(), "m", 2, []string{"ab"})
	require.NoError(t, err)
	assert.InDelta(t, 2, second[0][0], 0)
	second[0][1] = 77 // mutate the hit result

	third, err := c.Embed(t.Context(), "m", 2, []string{"ab"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{2, 2}}, third)
}

func TestCache_Embed_ConcurrentUse(t *testing.T) {
	t.Parallel()
	f := &fakeEmbedder{}
	c := newCache(t, f, 3)
	texts := []string{"a", "bb", "ccc", "dddd", "eeeee"}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				batch := []string{texts[(g+i)%5], texts[(g+i+1)%5], texts[(g+i)%5]}
				got, err := c.Embed(context.Background(), "m", 2, batch)
				if !assert.NoError(t, err) {
					return
				}
				for j, b := range batch {
					assert.InDelta(t, float32(len(b)), got[j][0], 0)
				}
			}
		}()
	}
	wg.Wait()
	st := c.Stats()
	assert.Equal(t, uint64(8*50*3), st.Hits+st.Misses)
	assert.LessOrEqual(t, c.Len(), 3)
}
