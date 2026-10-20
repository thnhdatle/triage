// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMode_ParseMode_Values(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{in: "off", want: ModeOff},
		{in: "mask", want: ModeMask},
		{in: "local_only", want: ModeLocalOnly},
		{in: "", wantErr: true},
		{in: "LOCAL_ONLY", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMode(tc.in)
			if tc.wantErr {
				require.EqualError(t, err, `privacy: unknown mode "`+tc.in+`" (want off, mask or local_only)`)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.True(t, got.Valid())
		})
	}
}

func TestMode_StricterThan_Order(t *testing.T) {
	t.Parallel()
	order := []Mode{ModeOff, ModeMask, ModeLocalOnly}
	for i, a := range order {
		for j, b := range order {
			assert.Equal(t, i > j, a.StricterThan(b), "%s > %s", a, b)
		}
	}
	bogus := Mode("bogus")
	assert.False(t, bogus.Valid())
	assert.False(t, bogus.StricterThan(ModeOff))
	assert.True(t, ModeOff.StricterThan(bogus))
}
