// SPDX-License-Identifier: Apache-2.0

package privacy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
)

func testMapping(t *testing.T) *Mapping {
	t.Helper()
	m := NewMapping(userReq("x"), []Finding{
		finding("email", "a@b.com"),
		finding("email", "c@d.org"),
		finding("kr_rrn", "900101-1234567"),
		finding("name", `say "hi" \ ok`),
	})
	require.Equal(t, map[string]string{
		"[[EMAIL_1]]": "a@b.com", "[[EMAIL_2]]": "c@d.org",
		"[[KR_RRN_1]]": "900101-1234567", "[[NAME_1]]": `say "hi" \ ok`,
	}, m.keys)
	return m
}

func TestMapping_Unmask_Text(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	tests := []struct {
		name        string
		in          string
		want        string
		wantPartial bool
	}{
		{name: "empty", in: "", want: ""},
		{name: "no placeholder", in: "plain text", want: "plain text"},
		{name: "exact", in: "mail [[EMAIL_1]].", want: "mail a@b.com."},
		{name: "several", in: "[[EMAIL_2]][[EMAIL_1]] [[KR_RRN_1]]", want: "c@d.orga@b.com 900101-1234567"},
		{name: "repeated", in: "[[EMAIL_1]] [[EMAIL_1]]", want: "a@b.com a@b.com"},
		{name: "text value not escaped", in: "[[NAME_1]]", want: `say "hi" \ ok`},
		{name: "extra brackets kept", in: "[[[EMAIL_1]]]", want: "[a@b.com]"},
		{name: "absent placeholders are fine", in: "only [[EMAIL_2]]", want: "only c@d.org"},
		{name: "unknown type is not partial", in: "[[PHONE_1]] [phone_1]", want: "[[PHONE_1]] [phone_1]"},
		{name: "lower case", in: "[[email_1]]", want: "[[email_1]]", wantPartial: true},
		{name: "single brackets", in: "[EMAIL_1]", want: "[EMAIL_1]", wantPartial: true},
		{name: "mixed brackets", in: "[[EMAIL_1]", want: "[[EMAIL_1]", wantPartial: true},
		{name: "inner spaces", in: "[[ EMAIL_1 ]]", want: "[[ EMAIL_1 ]]", wantPartial: true},
		{name: "unknown index", in: "[[EMAIL_9]]", want: "[[EMAIL_9]]", wantPartial: true},
		{name: "leading zero", in: "[[EMAIL_01]]", want: "[[EMAIL_01]]", wantPartial: true},
		{name: "partial after exact", in: "[[EMAIL_1]] [[KR_RRN_2]]", want: "a@b.com [[KR_RRN_2]]", wantPartial: true},
		{name: "kelvin sign folds to k", in: "[[KR_RRN_1]]", want: "[[KR_RRN_1]]", wantPartial: true},
		{name: "newline whitespace", in: "[\nemail_2\t]", want: "[\nemail_2\t]", wantPartial: true},
		{name: "not a token", in: "[[EMAIL_]] [[EMAIL 1]] [EMAIL-1]", want: "[[EMAIL_]] [[EMAIL 1]] [EMAIL-1]"},
		{name: "bracket at end", in: "x [", want: "x ["},
		{name: "prefix at end", in: "x [[EMAIL_", want: "x [[EMAIL_"},
		{name: "prefix then text", in: "[[EMAIL_1x [[KR", want: "[[EMAIL_1x [[KR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, partial := m.Unmask(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantPartial, partial)
		})
	}
}

func TestMapping_Unmask_EmptyMapping(t *testing.T) {
	t.Parallel()
	m := NewMapping(userReq("x"), nil)
	got, partial := m.Unmask("[[EMAIL_1]] [[email_1]]")
	assert.Equal(t, "[[EMAIL_1]] [[email_1]]", got)
	assert.False(t, partial)
	args := `{"a":"[[EMAIL_1]]"}`
	got, partial = m.UnmaskJSONString(args)
	assert.Equal(t, args, got)
	assert.False(t, partial)
}

func TestMapping_UnmaskJSONString_Escaping(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	tests := []struct {
		name        string
		in          string
		want        string
		wantPartial bool
	}{
		{name: "quote and backslash", in: `{"n":"[[NAME_1]]"}`, want: `{"n":"say \"hi\" \\ ok"}`},
		{name: "plain value", in: `{"to":"[[EMAIL_1]]"}`, want: `{"to":"a@b.com"}`},
		{name: "after escaped quote", in: `{"n":"x\"[[NAME_1]]"}`, want: `{"n":"x\"say \"hi\" \\ ok"}`},
		{name: "after escaped backslash", in: `{"n":"x\\","m":"[[NAME_1]]"}`, want: `{"n":"x\\","m":"say \"hi\" \\ ok"}`},
		{name: "object key", in: `{"[[NAME_1]]":1}`, want: `{"say \"hi\" \\ ok":1}`},
		{name: "outside string verbatim", in: `{"n":[[NAME_1]]}`, want: `{"n":say "hi" \ ok}`},
		{name: "backslash outside string ignored", in: `\"[[NAME_1]]"`, want: `\"say \"hi\" \\ ok"`},
		{name: "partial", in: `{"n":"[[name_1]]"}`, want: `{"n":"[[name_1]]"}`, wantPartial: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, partial := m.UnmaskJSONString(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantPartial, partial)
		})
	}
}

func TestMapping_UnmaskJSONString_DecodesToValue(t *testing.T) {
	t.Parallel()
	value := "q\" b\\ nl\n tab\t ctl\x01 html<>& é"
	req := userReq("x " + value)
	m := NewMapping(req, []Finding{finding("name", value)})
	got, partial := m.UnmaskJSONString(`{"name":"[[NAME_1]]","list":["[[NAME_1]]"]}`)
	require.False(t, partial)
	require.True(t, json.Valid([]byte(got)), got)
	var decoded struct {
		Name string   `json:"name"`
		List []string `json:"list"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &decoded))
	assert.Equal(t, value, decoded.Name)
	assert.Equal(t, []string{value}, decoded.List)
	assert.Contains(t, got, "<>&", "no HTML escaping")
}

func TestMapping_UnmaskResponse_Choices(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	resp := chat.Response{
		ID: "resp", Model: "m", Usage: chat.Usage{PromptTokens: 3},
		Choices: []chat.Choice{
			{Index: 0, FinishReason: chat.FinishStop, Message: chat.Message{
				Role: chat.RoleAssistant,
				Content: []chat.Part{
					{Type: chat.PartText, Text: "hi [[EMAIL_1]]"},
					{Type: chat.PartImage, ImageURL: "[[EMAIL_1]]"},
				},
			}},
			{Index: 1, FinishReason: chat.FinishToolCalls, Message: chat.Message{
				Role: chat.RoleAssistant,
				ToolCalls: []chat.ToolCall{
					{ID: "c1", Type: "function", Function: chat.FunctionCall{Name: "send", Arguments: `{"n":"[[NAME_1]]"}`}},
				},
			}},
		},
	}
	orig := resp
	orig.Choices = append([]chat.Choice(nil), resp.Choices...)
	got, partial := m.UnmaskResponse(resp)
	assert.False(t, partial)
	assert.Equal(t, orig, resp, "input not modified")
	assert.Equal(t, "hi [[EMAIL_1]]", resp.Choices[0].Message.Content[0].Text, "input parts not modified")
	maskedArgs := `{"n":"[[NAME_1]]"}`
	assert.Equal(t, maskedArgs, resp.Choices[1].Message.ToolCalls[0].Function.Arguments)

	want := chat.Response{
		ID: "resp", Model: "m", Usage: chat.Usage{PromptTokens: 3},
		Choices: []chat.Choice{
			{Index: 0, FinishReason: chat.FinishStop, Message: chat.Message{
				Role: chat.RoleAssistant,
				Content: []chat.Part{
					{Type: chat.PartText, Text: "hi a@b.com"},
					{Type: chat.PartImage, ImageURL: "[[EMAIL_1]]"},
				},
			}},
			{Index: 1, FinishReason: chat.FinishToolCalls, Message: chat.Message{
				Role: chat.RoleAssistant,
				ToolCalls: []chat.ToolCall{
					{ID: "c1", Type: "function", Function: chat.FunctionCall{Name: "send", Arguments: `{"n":"say \"hi\" \\ ok"}`}},
				},
			}},
		},
	}
	assert.Equal(t, want, got)
	assert.Nil(t, got.Choices[0].Message.ToolCalls)
	assert.Nil(t, got.Choices[1].Message.Content)
}

func TestMapping_UnmaskResponse_Partial(t *testing.T) {
	t.Parallel()
	m := testMapping(t)
	text := chat.Response{Choices: []chat.Choice{
		{Message: chat.Message{Content: []chat.Part{{Type: chat.PartText, Text: "ok"}}}},
		{Message: chat.Message{Content: []chat.Part{{Type: chat.PartText, Text: "[EMAIL_1]"}}}},
	}}
	_, partial := m.UnmaskResponse(text)
	assert.True(t, partial)

	args := chat.Response{Choices: []chat.Choice{{Message: chat.Message{
		ToolCalls: []chat.ToolCall{
			{Function: chat.FunctionCall{Arguments: `{"a":"[[EMAIL_9]]"}`}},
			{Function: chat.FunctionCall{Arguments: `{}`}},
		},
	}}}}
	_, partial = m.UnmaskResponse(args)
	assert.True(t, partial)

	none, partial := m.UnmaskResponse(chat.Response{ID: "x"})
	assert.False(t, partial)
	assert.Equal(t, chat.Response{ID: "x", Choices: []chat.Choice{}}, none)
}
