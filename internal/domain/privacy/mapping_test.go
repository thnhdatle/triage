// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func TestMapping_NewMapping_Numbering(t *testing.T) {
	t.Parallel()
	m := NewMapping(userReq("x"), []Finding{
		finding("email", "a@b.com"),
		finding("phone", "+123456789"),
		finding("email", "c@d.org"),
		finding("email", "a@b.com"),
		finding("ner", "a@b.com"),
		finding("email", ""),
		{Type: TypeNERUnavailable, Value: "ignored"},
		finding("credit_card", "4111111111111111"),
	})
	assert.Equal(t, map[string]string{
		"a@b.com":          "[[EMAIL_1]]",
		"+123456789":       "[[PHONE_1]]",
		"c@d.org":          "[[EMAIL_2]]",
		"4111111111111111": "[[CREDIT_CARD_1]]",
	}, m.values)
	assert.Equal(t, map[string]string{
		"[[EMAIL_1]]":       "a@b.com",
		"[[PHONE_1]]":       "+123456789",
		"[[EMAIL_2]]":       "c@d.org",
		"[[CREDIT_CARD_1]]": "4111111111111111",
	}, m.keys)
	assert.Equal(t, map[string]struct{}{"EMAIL": {}, "PHONE": {}, "CREDIT_CARD": {}}, m.tokens)
	assert.Equal(t, len("[[CREDIT_CARD_1]]"), m.maxKeyLen)
	assert.False(t, m.Empty())
}

func TestMapping_NewMapping_Collision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  chat.Request
		want string
	}{
		{name: "no collision", req: userReq("hello"), want: "[[EMAIL_1]]"},
		{name: "user text", req: userReq("I wrote [[EMAIL_1]] here"), want: "[[EMAIL_2]]"},
		{
			name: "unscanned system text",
			req:  chat.Request{Messages: []chat.Message{chat.TextMessage(chat.RoleSystem, "[[EMAIL_1]]")}},
			want: "[[EMAIL_2]]",
		},
		{
			name: "tool arguments, two indices",
			req:  chat.Request{Messages: []chat.Message{toolCallMsg(`{"a":"[[EMAIL_1]]"}`, `[[EMAIL_2]]`)}},
			want: "[[EMAIL_3]]",
		},
		{name: "bracket run", req: userReq("[[[EMAIL_1]]]"), want: "[[EMAIL_2]]"},
		{name: "lower case does not collide", req: userReq("[[email_1]]"), want: "[[EMAIL_1]]"},
		{name: "other type does not collide", req: userReq("[[PHONE_1]]"), want: "[[EMAIL_1]]"},
		{name: "near miss does not collide", req: userReq("[EMAIL_1] [[EMAIL_01]] [[EMAIL_1]"), want: "[[EMAIL_1]]"},
		{name: "gap kept", req: userReq("[[EMAIL_2]]"), want: "[[EMAIL_1]]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewMapping(tc.req, []Finding{finding("email", "a@b.com")})
			assert.Equal(t, tc.want, m.values["a@b.com"])
		})
	}
}

func TestMapping_NewMapping_CollisionThenNext(t *testing.T) {
	t.Parallel()
	m := NewMapping(userReq("[[EMAIL_1]] [[EMAIL_3]]"), []Finding{
		finding("email", "a@b.com"), finding("email", "c@d.org"), finding("email", "e@f.net"),
	})
	assert.Equal(t, "[[EMAIL_2]]", m.values["a@b.com"])
	assert.Equal(t, "[[EMAIL_4]]", m.values["c@d.org"])
	assert.Equal(t, "[[EMAIL_5]]", m.values["e@f.net"])
}

func TestMapping_Empty_NoValues(t *testing.T) {
	t.Parallel()
	m := NewMapping(userReq("a@b.com"), []Finding{{Type: TypeNERUnavailable}})
	assert.True(t, m.Empty())
	req := chat.Request{Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "a@b.com"), toolCallMsg(`{"a":1}`)}}
	assert.Equal(t, req, m.MaskRequest(req))
	assert.Equal(t, "a@b.com", m.maskJSON("a@b.com"))
	assert.False(t, m.Leaks(req))
}

func TestMapping_MaskRequest_AllScopes(t *testing.T) {
	t.Parallel()
	req := chat.Request{
		ID: "r1",
		Messages: []chat.Message{
			chat.TextMessage(chat.RoleSystem, "admin is a@b.com"),
			{Role: chat.RoleUser, Content: []chat.Part{
				{Type: chat.PartText, Text: "a@b.com and a@b.com, call +123456789"},
				{Type: chat.PartImage, ImageURL: "https://x/a@b.com"},
			}, Name: "a@b.com"},
			{
				Role:      chat.RoleAssistant,
				Content:   []chat.Part{{Type: chat.PartText, Text: "ok +123456789"}},
				ToolCalls: toolCallMsg(`{"to":"a@b.com","n":"+123456789"}`).ToolCalls,
			},
			{Role: chat.RoleTool, Content: []chat.Part{{Type: chat.PartText, Text: "sent to a@b.com"}}},
		},
	}
	orig := req.Clone()
	m := NewMapping(req, []Finding{finding("email", "a@b.com"), finding("phone", "+123456789")})
	got := m.MaskRequest(req)

	assert.Equal(t, orig, req, "input is not modified")
	assert.Equal(t, "r1", got.ID)
	assert.Equal(t, "admin is [[EMAIL_1]]", got.Messages[0].Content[0].Text)
	assert.Equal(t, "[[EMAIL_1]] and [[EMAIL_1]], call [[PHONE_1]]", got.Messages[1].Content[0].Text)
	assert.Equal(t, "https://x/a@b.com", got.Messages[1].Content[1].ImageURL, "images are never touched")
	assert.Equal(t, "a@b.com", got.Messages[1].Name, "names are not in scope")
	assert.Equal(t, "ok [[PHONE_1]]", got.Messages[2].Content[0].Text)
	assert.JSONEq(t, `{"to":"[[EMAIL_1]]","n":"[[PHONE_1]]"}`, got.Messages[2].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "sent to [[EMAIL_1]]", got.Messages[3].Content[0].Text)
	assert.False(t, m.Leaks(got))
	assert.True(t, m.Leaks(req))
}

func TestMapping_MaskRequest_LongestValueFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		findings []Finding
		text     string
		want     string
	}{
		{
			name:     "substring value",
			findings: []Finding{finding("domain", "example.com"), finding("email", "x@example.com")},
			text:     "x@example.com and example.com",
			want:     "[[EMAIL_1]] and [[DOMAIN_1]]",
		},
		{
			name:     "overlapping values, leftmost wins",
			findings: []Finding{finding("a", "abc"), finding("b", "cde")},
			text:     "abcde cde",
			want:     "[[A_1]]de [[B_1]]",
		},
		{
			name:     "equal length ordered bytewise",
			findings: []Finding{finding("b", "xy"), finding("a", "xy2")},
			text:     "xy2",
			want:     "[[A_1]]",
		},
		{
			name:     "value made of placeholder bytes",
			findings: []Finding{finding("email", "a@b.com"), finding("ner", "EMAIL")},
			text:     "EMAIL a@b.com",
			want:     "[[NER_1]] [[EMAIL_1]]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := userReq(tc.text)
			m := NewMapping(req, tc.findings)
			got := m.MaskRequest(req)
			assert.Equal(t, tc.want, got.Messages[0].Content[0].Text)
			back, partial := m.Unmask(got.Messages[0].Content[0].Text)
			assert.Equal(t, tc.text, back)
			assert.False(t, partial)
		})
	}
}

func TestMapping_MaskJSON_Arguments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		findings []Finding
		args     string
		want     string
	}{
		{
			name:     "string value",
			findings: []Finding{finding("email", "a@b.com")},
			args:     `{"to": "a@b.com", "cc": ["x a@b.com y"]}`,
			want:     `{"to": "[[EMAIL_1]]", "cc": ["x [[EMAIL_1]] y"]}`,
		},
		{
			name:     "object key",
			findings: []Finding{finding("email", "a@b.com")},
			args:     `{"a@b.com":true}`,
			want:     `{"[[EMAIL_1]]":true}`,
		},
		{
			name:     "escaped form of a value with quote and backslash",
			findings: []Finding{finding("name", `O"Brien\x`)},
			args:     `{"n":"Mr O\"Brien\\x."}`,
			want:     `{"n":"Mr [[NAME_1]]."}`,
		},
		{
			name:     "bare number becomes a string",
			findings: []Finding{finding("credit_card", "4111111111111111")},
			args:     `{"card":4111111111111111,"n":41111111111111112,"ok":true}`,
			want:     `{"card":"[[CREDIT_CARD_1]]","n":"[[CREDIT_CARD_1]]2","ok":true}`,
		},
		{
			name:     "top level number",
			findings: []Finding{finding("ssn", "123456789")},
			args:     `123456789`,
			want:     `"[[SSN_1]]"`,
		},
		{
			name:     "match widened over an escape it starts in",
			findings: []Finding{finding("email", "nathan@x.com")},
			args:     `{"t":"hi\nathan@x.com!"}`,
			want:     `{"t":"hi[[EMAIL_1]]!"}`,
		},
		{
			name:     "match widened over a unicode escape it ends in",
			findings: []Finding{finding("id", "ab\\u00")},
			args:     `{"t":"xab\u00e9y"}`,
			want:     `{"t":"x[[ID_1]]y"}`,
		},
		{
			name:     "match widened over a utf8 sequence",
			findings: []Finding{finding("id", "\xa9x")},
			args:     `{"t":"aéxb"}`,
			want:     `{"t":"a[[ID_1]]b"}`,
		},
		{
			name:     "escaped quotes are not string ends",
			findings: []Finding{finding("email", "a@b.com")},
			args:     `{"t":"\"a@b.com\" \\","u":"a@b.com"}`,
			want:     `{"t":"\"[[EMAIL_1]]\" \\","u":"[[EMAIL_1]]"}`,
		},
		{
			name:     "no value present is unchanged",
			findings: []Finding{finding("email", "a@b.com")},
			args:     `{ "t" : [1, 2.5e3, null, false] }`,
			want:     `{ "t" : [1, 2.5e3, null, false] }`,
		},
		{
			name:     "only other strings untouched",
			findings: []Finding{finding("email", "a@b.com")},
			args:     `{"x":"\u00e9","t":"a@b.com"}`,
			want:     `{"x":"\u00e9","t":"[[EMAIL_1]]"}`,
		},
		{
			name:     "invalid json is masked as text",
			findings: []Finding{finding("name", `O"B`)},
			args:     `{"t":"a@b O"B O\"B`,
			want:     `{"t":"a@b [[NAME_1]] [[NAME_1]]`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := chat.Request{Messages: []chat.Message{toolCallMsg(tc.args)}}
			m := NewMapping(req, tc.findings)
			got := m.MaskRequest(req).Messages[0].ToolCalls[0].Function.Arguments
			assert.Equal(t, tc.want, got)
			if json.Valid([]byte(tc.args)) {
				assert.True(t, json.Valid([]byte(got)), "valid JSON stays valid")
			}
		})
	}
}

func TestMapping_Leaks_Detection(t *testing.T) {
	t.Parallel()
	m := NewMapping(userReq("x"), []Finding{finding("name", `O"B`)})
	tests := []struct {
		name string
		req  chat.Request
		want bool
	}{
		{name: "clean", req: userReq("hello"), want: false},
		{name: "raw in text", req: userReq(`hi O"B`), want: true},
		{name: "escaped in text is not a leak", req: userReq(`hi O\"B`), want: false},
		{name: "raw in arguments", req: chat.Request{Messages: []chat.Message{toolCallMsg(`x O"B`)}}, want: true},
		{name: "escaped in arguments", req: chat.Request{Messages: []chat.Message{toolCallMsg(`{"n":"O\"B"}`)}}, want: true},
		{
			name: "image url is not checked",
			req: chat.Request{Messages: []chat.Message{{
				Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartImage, ImageURL: `O"B`, Text: `O"B`}},
			}}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, m.Leaks(tc.req))
		})
	}
}

func TestMapping_Leaks_PathologicalValue(t *testing.T) {
	t.Parallel()
	req := userReq("a@b.com 1]]")
	m := NewMapping(req, []Finding{finding("email", "a@b.com"), finding("x", "1]]")})
	masked := m.MaskRequest(req)
	assert.Equal(t, "[[EMAIL_1]] [[X_1]]", masked.Messages[0].Content[0].Text)
	assert.True(t, m.Leaks(masked), "the value 1]] reappears inside the placeholders")
}

func TestMapping_Rescan_Continuation(t *testing.T) {
	t.Parallel()
	req := userReq("write to a@b.com, not [[EMAIL_2]]")
	m := NewMapping(req, []Finding{finding("email", "a@b.com")})
	require.Equal(t, "[[EMAIL_1]]", m.values["a@b.com"])

	text := "Sure: a@b.com, c@d.org, e@f.net, [[EMAIL_3]] and +123456789."
	got, err := m.Rescan(context.Background(), []Detector{emailDetector(), phoneDetector()}, text)
	require.NoError(t, err)
	assert.Equal(t, "Sure: [[EMAIL_1]], [[EMAIL_4]], [[EMAIL_5]], [[EMAIL_3]] and [[PHONE_1]].", got)
	assert.Equal(t, "[[EMAIL_4]]", m.values["c@d.org"])
	assert.Equal(t, "[[EMAIL_5]]", m.values["e@f.net"])

	out, partial := m.Unmask("[[EMAIL_4]] [[PHONE_1]] [[EMAIL_1]]")
	assert.Equal(t, "c@d.org +123456789 a@b.com", out)
	assert.False(t, partial)
	assert.Equal(t, "write to [[EMAIL_1]], not [[EMAIL_2]]", m.MaskRequest(req).Messages[0].Content[0].Text)

	again, err := m.Rescan(context.Background(), []Detector{emailDetector()}, "g@h.io")
	require.NoError(t, err)
	assert.Equal(t, "[[EMAIL_6]]", again)
}

func TestMapping_Rescan_EmptyMappingGrows(t *testing.T) {
	t.Parallel()
	m := NewMapping(userReq("nothing"), nil)
	require.True(t, m.Empty())
	got, err := m.Rescan(context.Background(), []Detector{emailDetector()}, "x a@b.com")
	require.NoError(t, err)
	assert.Equal(t, "x [[EMAIL_1]]", got)
	assert.False(t, m.Empty())
	out, partial := m.Unmask("[[email_1]]")
	assert.Equal(t, "[[email_1]]", out)
	assert.True(t, partial)
}

func TestMapping_Rescan_Failures(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name      string
		detectors []Detector
		wantIs    error
		wantMsg   string
	}{
		{
			name:      "detector error",
			detectors: []Detector{emailDetector(), staticDetector{id: "bad", err: boom}},
			wantIs:    boom,
			wantMsg:   "privacy: detector bad: boom",
		},
		{
			name: "ner unavailable",
			detectors: []Detector{
				emailDetector(),
				staticDetector{id: "ner", findings: []Finding{{DetectorID: "ner", Type: TypeNERUnavailable}}},
			},
			wantIs:  ErrRescanIncomplete,
			wantMsg: "privacy: continuation text could not be fully scanned (detector ner)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewMapping(userReq("a@b.com"), []Finding{finding("email", "a@b.com")})
			got, err := m.Rescan(context.Background(), tc.detectors, "c@d.org a@b.com")
			require.ErrorIs(t, err, tc.wantIs)
			assert.Equal(t, tc.wantMsg, err.Error())
			assert.Empty(t, got)
			assert.Equal(t, map[string]string{"a@b.com": "[[EMAIL_1]]"}, m.values, "mapping unchanged")
			assert.NotContains(t, m.prefixes, "[[EMAIL_2")
		})
	}
}

func TestMapping_Rescan_FillsValueFromSpan(t *testing.T) {
	t.Parallel()
	d := regexDetector{id: "e", typ: "email", re: regexp.MustCompile(`c@d\.org`), noValue: true}
	m := NewMapping(userReq("x"), nil)
	got, err := m.Rescan(context.Background(), []Detector{d}, "to c@d.org")
	require.NoError(t, err)
	assert.Equal(t, "to [[EMAIL_1]]", got)
}

func TestMapping_Rescan_NothingFound(t *testing.T) {
	t.Parallel()
	m := NewMapping(userReq("x"), nil)
	got, err := m.Rescan(context.Background(), []Detector{emailDetector()}, "no pii [[EMAIL_1]]")
	require.NoError(t, err)
	assert.Equal(t, "no pii [[EMAIL_1]]", got)
	assert.True(t, m.Empty())
}

func TestMapping_MaskJSON_EscapedFormEqualsOtherValue(t *testing.T) {
	t.Parallel()
	raw := `a\"b`
	req := chat.Request{Messages: []chat.Message{toolCallMsg(`{"x":"a\"b"}`)}}
	m := NewMapping(req, []Finding{finding("name", raw), finding("name", `a"b`)})
	require.Equal(t, "[[NAME_1]]", m.values[raw])
	require.Equal(t, "[[NAME_2]]", m.values[`a"b`])
	want := `{"x":"[[NAME_1]]"}`
	assert.Equal(t, want, m.MaskRequest(req).Messages[0].ToolCalls[0].Function.Arguments)
	olds := make([]string, 0, len(m.argForms['a']))
	for _, f := range m.argForms['a'] {
		olds = append(olds, f.old)
	}
	assert.Equal(t, []string{`a\\\"b`, raw, `a"b`}, olds, "longest first, duplicate form dropped")
}

func TestMapping_NewMapping_DerivedTables(t *testing.T) {
	t.Parallel()
	empty := NewMapping(userReq("x"), nil)
	assert.Nil(t, empty.textReplacer)
	assert.Nil(t, empty.argReplacer)
	assert.Nil(t, empty.partialRe())
	assert.Empty(t, empty.prefixes)
	assert.Zero(t, empty.maxKeyLen)

	m := NewMapping(userReq("x"), []Finding{finding("a", "v1"), finding("a", "v2")})
	assert.Equal(t, map[string]struct{}{
		"[": {}, "[[": {}, "[[A": {}, "[[A_": {},
		"[[A_1": {}, "[[A_1]": {}, "[[A_2": {}, "[[A_2]": {},
	}, m.prefixes)
	assert.Equal(t, 7, m.maxKeyLen)
	assert.NotNil(t, m.textReplacer)
}
