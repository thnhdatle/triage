// SPDX-License-Identifier: Apache-2.0

package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_RealFile(t *testing.T) {
	t.Parallel()
	f, err := Load("criteria.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, f.Criteria)
	seen := map[string]bool{}
	for _, c := range f.Criteria {
		assert.False(t, seen[c.ID], "duplicate id %s", c.ID)
		seen[c.ID] = true
		assert.NotEmpty(t, c.Test, c.ID)
		for _, env := range []string{EnvMac, EnvCI} {
			vals, skip := c.Thresholds(env)
			assert.True(t, skip || len(vals) > 0, "%s has no pass.%s", c.ID, env)
		}
	}
}

func TestThresholds(t *testing.T) {
	t.Parallel()
	const doc = `
project: x
criteria:
  - id: A-1
    test: TestA
    pass:
      mac: { n: 3 }
      ci: skip
  - id: A-2
    test: TestB
    pass:
      mac: { n: 1 }
      ci: { n: 2 }
`
	path := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	f, err := Load(path)
	require.NoError(t, err)
	tests := []struct {
		name     string
		id, env  string
		wantN    any
		wantSkip bool
	}{
		{"mac values", "A-1", EnvMac, 3, false},
		{"ci skip", "A-1", EnvCI, nil, true},
		{"ci values", "A-2", EnvCI, 2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, ok := f.Find(tc.id)
			require.True(t, ok)
			vals, skip := c.Thresholds(tc.env)
			assert.Equal(t, tc.wantSkip, skip)
			assert.Equal(t, tc.wantN, vals["n"])
		})
	}
	_, ok := f.Find("nope")
	assert.False(t, ok)
}

func TestLoad_Errors(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("criteria:\n  - id: a\n    pass: { mac: oops }\n"), 0o600))
	_, err = Load(path)
	require.Error(t, err)
}
