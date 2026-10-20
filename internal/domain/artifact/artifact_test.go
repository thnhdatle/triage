// SPDX-License-Identifier: Apache-2.0

package artifact_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/thnhdatle/triage/internal/domain/artifact"
)

func TestManifest_Entry_Table(t *testing.T) {
	t.Parallel()
	m := artifact.Manifest{
		SchemaVersion: 1,
		Entries: []artifact.Entry{
			{Name: "bert", Revision: "r1"},
			{Name: "mf", Revision: "r2"},
		},
	}
	tests := []struct {
		name   string
		lookup string
		want   artifact.Entry
		found  bool
	}{
		{name: "first", lookup: "bert", want: m.Entries[0], found: true},
		{name: "second", lookup: "mf", want: m.Entries[1], found: true},
		{name: "missing", lookup: "nope", want: artifact.Entry{}, found: false},
		{name: "empty manifest name", lookup: "", want: artifact.Entry{}, found: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := m.Entry(tc.lookup)
			assert.Equal(t, tc.found, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestManifest_Entry_EmptyManifest(t *testing.T) {
	t.Parallel()
	_, ok := artifact.Manifest{}.Entry("x")
	assert.False(t, ok)
}

func TestErrors_Sentinels_Distinct(t *testing.T) {
	t.Parallel()
	errs := []error{artifact.ErrChecksum, artifact.ErrNotAllowed, artifact.ErrMissing}
	for i, a := range errs {
		assert.Contains(t, a.Error(), "artifact: ")
		for j, b := range errs {
			assert.Equal(t, i == j, errors.Is(a, b))
		}
	}
}
