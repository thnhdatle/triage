// SPDX-License-Identifier: Apache-2.0

package piiregex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/privacy"
)

func scanAll(t *testing.T, text string) []privacy.Finding {
	t.Helper()
	d, err := New(WithStreetAddress(true))
	require.NoError(t, err)
	fs, err := d.Detect(context.Background(), text)
	require.NoError(t, err)
	return fs
}

func scanOne(t *testing.T, id, text string) []privacy.Finding {
	t.Helper()
	d, err := New(WithStreetAddress(true))
	require.NoError(t, err)
	for _, sub := range d.Detectors() {
		if sub.ID() == id {
			fs, err := sub.Detect(context.Background(), text)
			require.NoError(t, err)
			for _, f := range fs {
				require.Equal(t, text[f.Start:f.End], f.Value)
				require.Equal(t, id, f.DetectorID)
			}
			return fs
		}
	}
	require.Failf(t, "unknown detector", "id %q", id)
	return nil
}

func values(fs []privacy.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Value)
	}
	return out
}
