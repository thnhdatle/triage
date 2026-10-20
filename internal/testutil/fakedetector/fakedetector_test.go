// SPDX-License-Identifier: Apache-2.0

package fakedetector_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/testutil/fakedetector"
)

var _ privacy.Detector = (*fakedetector.Detector)(nil)

func finding(typ string, start, end int, value string) privacy.Finding {
	return privacy.Finding{DetectorID: "fake", Type: typ, Start: start, End: end, Confidence: 1, Value: value}
}

func TestDetector_Detect_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    *fakedetector.Detector
		text string
		want []privacy.Finding
	}{
		{name: "literal none", d: fakedetector.NewLiteral("fake", "email", "bob"), text: "alice", want: nil},
		{
			name: "literal repeated",
			d:    fakedetector.NewLiteral("fake", "email", "bob"),
			text: "bob and bob",
			want: []privacy.Finding{finding("email", 0, 3, "bob"), finding("email", 8, 11, "bob")},
		},
		{
			name: "two literals sorted by start",
			d:    fakedetector.NewLiteral("fake", "name", "zed", "amy"),
			text: "amy met zed",
			want: []privacy.Finding{finding("name", 0, 3, "amy"), finding("name", 8, 11, "zed")},
		},
		{
			name: "overlapping literals longer first",
			d:    fakedetector.NewLiteral("fake", "name", "ab", "abc"),
			text: "abc",
			want: []privacy.Finding{finding("name", 0, 3, "abc"), finding("name", 0, 2, "ab")},
		},
		{name: "empty literal ignored", d: fakedetector.NewLiteral("fake", "x", ""), text: "abc", want: nil},
		{
			name: "regexp matches",
			d:    fakedetector.NewRegexp("fake", "digits", regexp.MustCompile(`\d+`)),
			text: "a12 b345",
			want: []privacy.Finding{finding("digits", 1, 3, "12"), finding("digits", 5, 8, "345")},
		},
		{
			name: "regexp empty matches ignored",
			d:    fakedetector.NewRegexp("fake", "digits", regexp.MustCompile(`\d*`)),
			text: "ab",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.d.Detect(context.Background(), tc.text)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDetector_Detect_ErrorAndRecording(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	d := fakedetector.NewLiteral("fake", "email", "x")
	assert.Equal(t, "fake", d.ID())
	assert.Same(t, d, d.WithError(boom))
	_, err := d.Detect(context.Background(), "one")
	require.ErrorIs(t, err, boom)
	d.WithError(nil)
	_, err = d.Detect(context.Background(), "two")
	require.NoError(t, err)
	assert.Equal(t, 2, d.Calls())
	assert.Equal(t, []string{"one", "two"}, d.Texts())
}

func TestDetector_Detect_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fakedetector.NewLiteral("fake", "email", "x").Detect(ctx, "x")
	require.ErrorIs(t, err, context.Canceled)
}
