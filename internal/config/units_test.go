// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/config"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "30s", want: 30 * time.Second},
		{in: "200ms", want: 200 * time.Millisecond},
		{in: "14d", want: 14 * 24 * time.Hour},
		{in: "1d12h", want: 36 * time.Hour},
		{in: " 2d ", want: 48 * time.Hour},
		{in: "0d", want: 0},
		{in: "", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "5", wantErr: true},
		{in: "d", wantErr: true},
		{in: "1dx", wantErr: true},
		{in: "99999999999d", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := config.ParseDuration(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "1048576", want: 1 << 20},
		{in: "8MiB", want: 8 << 20},
		{in: "256KiB", want: 256 << 10},
		{in: "1GiB", want: 1 << 30},
		{in: "5kb", want: 5000},
		{in: "2 MB", want: 2_000_000},
		{in: "3GB", want: 3_000_000_000},
		{in: "7B", want: 7},
		{in: "", wantErr: true},
		{in: "MiB", wantErr: true},
		{in: "1.5MiB", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "8XiB", wantErr: true},
		{in: "99999999999999GiB", wantErr: true},
		{in: "99999999999999999999", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := config.ParseSize(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestUnitsYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	type doc struct {
		D config.Duration `yaml:"d"`
		S config.Size     `yaml:"s"`
	}
	var in doc
	require.NoError(t, yaml.Unmarshal([]byte("d: 2d\ns: 1KiB\n"), &in))
	assert.Equal(t, 48*time.Hour, in.D.Std())
	assert.Equal(t, int64(1024), in.S.Bytes())

	out, err := yaml.Marshal(in)
	require.NoError(t, err)
	var back doc
	require.NoError(t, yaml.Unmarshal(out, &back))
	assert.Equal(t, in, back)
}
