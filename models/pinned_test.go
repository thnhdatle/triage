// SPDX-License-Identifier: Apache-2.0

package pinned_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pinned "github.com/thnhdatle/triage/models"
)

func TestManifest_Entries(t *testing.T) {
	t.Parallel()
	m, err := pinned.Manifest()
	require.NoError(t, err)
	for _, name := range []string{"bert", "mf", "ner"} {
		e, ok := m.Entry(name)
		require.True(t, ok, name)
		assert.Len(t, e.Revision, 40, name)
		assert.NotEmpty(t, e.Files, name)
	}
}

func TestParseManifest_Errors(t *testing.T) {
	t.Parallel()
	_, err := pinned.ParseManifest([]byte("entries: []\n"))
	require.Error(t, err)
	_, err = pinned.ParseManifest([]byte("entries: {bad"))
	require.Error(t, err)
}

func TestBERTGraph_IsCopy(t *testing.T) {
	t.Parallel()
	g := pinned.BERTGraph()
	require.NotEmpty(t, g)
	g[0] ^= 0xff
	assert.NotEqual(t, g[0], pinned.BERTGraph()[0])
}
