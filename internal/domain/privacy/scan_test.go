// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func TestParseOversizeAction_Parse_Values(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    OversizeAction
		wantErr bool
	}{
		{name: "local only", in: "local_only", want: OversizeLocalOnly},
		{name: "reject", in: "reject", want: OversizeReject},
		{name: "empty", in: "", wantErr: true},
		{name: "unknown", in: "mask", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseOversizeAction(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), `privacy: unknown on_oversize "`+tc.in+`"`)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func scopeRequest() chat.Request {
	return chat.Request{Messages: []chat.Message{
		chat.TextMessage(chat.RoleSystem, "sys"),
		{Role: chat.RoleUser, Content: []chat.Part{
			{Type: chat.PartText, Text: "u1"},
			{Type: chat.PartImage, ImageURL: "data:image/png;base64,QUJD", Text: "img-text"},
			{Type: chat.PartText, Text: ""},
			{Type: chat.PartText, Text: "u2"},
		}},
		{
			Role:      chat.RoleAssistant,
			Content:   []chat.Part{{Type: chat.PartText, Text: "a1"}},
			ToolCalls: toolCallMsg(`{"q":"x"}`, "").ToolCalls,
		},
		{Role: chat.RoleTool, Content: []chat.Part{{Type: chat.PartText, Text: "t1"}}, ToolCallID: "call"},
		chat.TextMessage(chat.Role("developer"), "d1"),
	}}
}

func TestScan_Scan_Scope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		scanSystem bool
		want       []string
	}{
		{name: "system excluded", want: []string{"u1", "u2", "a1", `{"q":"x"}`, "t1", "d1"}},
		{name: "system included", scanSystem: true, want: []string{"sys", "u1", "u2", "a1", `{"q":"x"}`, "t1", "d1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingDetector{}
			res, err := Scan(context.Background(), []Detector{rec}, scopeRequest(), ScanOptions{ScanSystem: tc.scanSystem})
			require.NoError(t, err)
			assert.Equal(t, tc.want, rec.texts)
			assert.False(t, res.HasFindings())
			assert.NotNil(t, res.Counts)
			assert.Empty(t, res.Counts)
		})
	}
}

func TestScan_Scan_Findings(t *testing.T) {
	t.Parallel()
	req := chat.Request{Messages: []chat.Message{
		chat.TextMessage(chat.RoleUser, "mail a@b.com or c@d.org, call +123456789"),
		toolCallMsg(`{"to":"e@f.net"}`),
	}}
	noValue := phoneDetector()
	noValue.noValue = true
	res, err := Scan(context.Background(), []Detector{emailDetector(), noValue}, req, ScanOptions{})
	require.NoError(t, err)
	want := []Finding{
		{DetectorID: "email", Type: "email", Start: 5, End: 12, Confidence: 1, Value: "a@b.com"},
		{DetectorID: "email", Type: "email", Start: 16, End: 23, Confidence: 1, Value: "c@d.org"},
		{DetectorID: "phone", Type: "phone", Start: 30, End: 40, Confidence: 1, Value: "+123456789"},
		{DetectorID: "email", Type: "email", Start: 7, End: 14, Confidence: 1, Value: "e@f.net"},
	}
	assert.Equal(t, want, res.Findings)
	assert.Equal(t, map[string]int{"email": 3, "phone": 1}, res.Counts)
	assert.False(t, res.Oversize)
	assert.True(t, res.HasFindings())
	assert.False(t, res.Constrained())
}

func TestScan_Scan_ValueAndSpan(t *testing.T) {
	t.Parallel()
	text := "0123456789"
	tests := []struct {
		name    string
		f       Finding
		want    string
		wantErr string
	}{
		{name: "value kept", f: Finding{Type: "x", Start: 0, End: 1, Value: "given"}, want: "given"},
		{name: "span filled", f: Finding{Type: "x", Start: 2, End: 5}, want: "234"},
		{name: "whole text", f: Finding{Type: "x", Start: 0, End: 10}, want: text},
		{name: "ner unavailable without value", f: Finding{Type: TypeNERUnavailable}, want: ""},
		{name: "negative start", f: Finding{Type: "x", Start: -1, End: 2}, wantErr: "span [-1,2) is invalid"},
		{name: "empty span", f: Finding{Type: "x", Start: 3, End: 3}, wantErr: "span [3,3) is invalid"},
		{name: "end beyond text", f: Finding{Type: "x", Start: 3, End: 11}, wantErr: "span [3,11) is invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := staticDetector{id: "st", findings: []Finding{tc.f}}
			res, err := Scan(context.Background(), []Detector{d}, userReq(text), ScanOptions{})
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, "privacy: detector st: x finding has no value and "+tc.wantErr, err.Error())
				assert.Empty(t, res.Findings)
				assert.NotNil(t, res.Counts)
				return
			}
			require.NoError(t, err)
			require.Len(t, res.Findings, 1)
			assert.Equal(t, tc.want, res.Findings[0].Value)
		})
	}
}

func TestScan_Scan_DetectorError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	ok := staticDetector{id: "ok", findings: []Finding{finding("email", "a@b.com")}}
	detectors := []Detector{ok, staticDetector{id: "bad", err: boom}}
	res, err := Scan(context.Background(), detectors, userReq("x"), ScanOptions{})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, "privacy: detector bad: boom", err.Error())
	assert.Empty(t, res.Findings)
	assert.Empty(t, res.Counts)
	assert.NotNil(t, res.Counts)
	assert.False(t, res.HasFindings())
}

func TestScan_Scan_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &recordingDetector{}
	_, err := Scan(ctx, []Detector{rec}, userReq("x"), ScanOptions{})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "privacy: detector rec: context canceled", err.Error())
	assert.Empty(t, rec.texts)
}

func TestScan_Scan_Oversize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		texts        []string
		opts         ScanOptions
		wantOversize bool
		wantErr      bool
	}{
		{name: "at limit", texts: []string{"abc", "de"}, opts: ScanOptions{MaxScanBytes: 5}},
		{
			name: "over limit local only", texts: []string{"abc", "def"},
			opts: ScanOptions{MaxScanBytes: 5}, wantOversize: true,
		},
		{
			name: "over limit explicit local only", texts: []string{"abcdef"},
			opts: ScanOptions{MaxScanBytes: 5, OnOversize: OversizeLocalOnly}, wantOversize: true,
		},
		{
			name: "over limit reject", texts: []string{"abcdef"},
			opts: ScanOptions{MaxScanBytes: 5, OnOversize: OversizeReject}, wantOversize: true, wantErr: true,
		},
		{name: "default limit at", texts: []string{strings.Repeat("a", DefaultMaxScanBytes)}},
		{name: "default limit over", texts: []string{strings.Repeat("a", DefaultMaxScanBytes+1)}, wantOversize: true},
		{
			name: "negative means default", texts: []string{strings.Repeat("a", DefaultMaxScanBytes)},
			opts: ScanOptions{MaxScanBytes: -1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingDetector{}
			res, err := Scan(context.Background(), []Detector{rec}, userReq(tc.texts...), tc.opts)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrScanLimit)
				assert.Equal(t, "privacy: scoped text exceeds max_scan_bytes (6 > 5 bytes)", err.Error())
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantOversize, res.Oversize)
			assert.Equal(t, tc.wantOversize, res.HasFindings())
			assert.Equal(t, tc.wantOversize, res.Constrained())
			assert.NotNil(t, res.Counts)
			if tc.wantOversize {
				assert.Empty(t, rec.texts, "nothing is scanned when oversize")
			} else {
				assert.Equal(t, tc.texts, rec.texts)
			}
		})
	}
}

func TestScan_Scan_OversizeIgnoresUnscopedText(t *testing.T) {
	t.Parallel()
	req := chat.Request{Messages: []chat.Message{
		chat.TextMessage(chat.RoleSystem, strings.Repeat("s", 10)),
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartImage, ImageURL: strings.Repeat("i", 10)}}},
		chat.TextMessage(chat.RoleUser, "abc"),
	}}
	res, err := Scan(context.Background(), nil, req, ScanOptions{MaxScanBytes: 3})
	require.NoError(t, err)
	assert.False(t, res.Oversize)
	res, err = Scan(context.Background(), nil, req, ScanOptions{MaxScanBytes: 3, ScanSystem: true})
	require.NoError(t, err)
	assert.True(t, res.Oversize)
}

func TestResult_RequiresLocal_Modes(t *testing.T) {
	t.Parallel()
	ordinary := Result{Findings: []Finding{finding("email", "a@b.com")}, Counts: map[string]int{"email": 1}}
	ner := Result{Findings: []Finding{{Type: TypeNERUnavailable}}, Counts: map[string]int{TypeNERUnavailable: 1}}
	oversize := Result{Counts: map[string]int{}, Oversize: true}
	none := Result{Counts: map[string]int{}}
	tests := []struct {
		name            string
		r               Result
		wantHas         bool
		wantConstrained bool
		want            map[Mode]bool
	}{
		{
			name: "none", r: none,
			want: map[Mode]bool{ModeOff: false, ModeMask: false, ModeLocalOnly: false, Mode("bogus"): false},
		},
		{
			name: "ordinary finding", r: ordinary, wantHas: true,
			want: map[Mode]bool{ModeOff: false, ModeMask: false, ModeLocalOnly: true, Mode("bogus"): false},
		},
		{
			name: "ner unavailable", r: ner, wantHas: true, wantConstrained: true,
			want: map[Mode]bool{ModeOff: false, ModeMask: true, ModeLocalOnly: true, Mode("bogus"): false},
		},
		{
			name: "oversize", r: oversize, wantHas: true, wantConstrained: true,
			want: map[Mode]bool{ModeOff: false, ModeMask: true, ModeLocalOnly: true, Mode("bogus"): false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantHas, tc.r.HasFindings())
			assert.Equal(t, tc.wantConstrained, tc.r.Constrained())
			for mode, want := range tc.want {
				assert.Equal(t, want, tc.r.RequiresLocal(mode), mode)
			}
		})
	}
}
