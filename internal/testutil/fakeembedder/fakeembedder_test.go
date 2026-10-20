// SPDX-License-Identifier: Apache-2.0

package fakeembedder_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/testutil/fakeembedder"
)

var _ difficulty.Embedder = (*fakeembedder.Embedder)(nil)

func TestEmbedder_Embed_DeterministicUnitVectors(t *testing.T) {
	t.Parallel()
	e := fakeembedder.New(16)
	got, err := e.Embed(context.Background(), "m", 0, []string{"a", "b", "a"})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, got[0], got[2])
	assert.NotEqual(t, got[0], got[1])
	for _, v := range got {
		require.Len(t, v, 16)
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		assert.InDelta(t, 1, math.Sqrt(sum), 1e-5)
	}
	again, err := fakeembedder.New(16).Embed(context.Background(), "other", 0, []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, got[0], again[0])
}

func TestEmbedder_Embed_Dimensions_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ctor int
		req  int
		want int
	}{
		{name: "default dims", ctor: 0, req: 0, want: fakeembedder.DefaultDimensions},
		{name: "constructor dims", ctor: 4, req: 0, want: 4},
		{name: "request overrides", ctor: 4, req: 20, want: 20},
		{name: "more than one hash block", ctor: 33, req: 0, want: 33},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := fakeembedder.New(tc.ctor).Embed(context.Background(), "m", tc.req, []string{"x"})
			require.NoError(t, err)
			assert.Len(t, got[0], tc.want)
		})
	}
}

func TestEmbedder_Embed_EmptyInput(t *testing.T) {
	t.Parallel()
	got, err := fakeembedder.New(4).Embed(context.Background(), "m", 0, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestEmbedder_Embed_CountsCallsAndTexts(t *testing.T) {
	t.Parallel()
	e := fakeembedder.New(4)
	_, err := e.Embed(context.Background(), "m", 0, []string{"a", "b"})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), "m", 0, []string{"c"})
	require.NoError(t, err)
	assert.Equal(t, 2, e.Calls())
	assert.Equal(t, []string{"a", "b", "c"}, e.Texts())
}

func TestEmbedder_Embed_ConfiguredError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	e := fakeembedder.New(4)
	e.SetError(boom)
	_, err := e.Embed(context.Background(), "m", 0, []string{"a"})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, e.Calls())
	e.SetError(nil)
	_, err = e.Embed(context.Background(), "m", 0, []string{"a"})
	require.NoError(t, err)
}

func TestEmbedder_Embed_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fakeembedder.New(4).Embed(ctx, "m", 0, []string{"a"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestVector_NonPositiveSize_Empty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, fakeembedder.Vector("x", 0))
}
