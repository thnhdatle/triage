// SPDX-License-Identifier: Apache-2.0

package httpapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/httpapi"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

func TestNew_MissingDependency_Error(t *testing.T) {
	t.Parallel()
	r, c, id := newFakeRouter(), fakeCatalog{}, idGen()
	_, err := httpapi.New(nil, c, id)
	require.Error(t, err)
	_, err = httpapi.New(r, nil, id)
	require.Error(t, err)
	_, err = httpapi.New(r, c, nil)
	require.Error(t, err)
}

func TestChat_Success_BodyAndHeaders(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	h := newServer(t, fr, httpapi.WithSettings(func() httpapi.Settings {
		return httpapi.Settings{ExposeDecisionDetail: true}
	})).API()

	rec := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "dec-1", rec.Header().Get("X-Triage-Decision"))
	assert.Equal(t, "auto", rec.Header().Get("X-Triage-Route"))
	assert.Equal(t, "local-weak", rec.Header().Get("X-Triage-Target"))
	assert.Equal(t, "rule:length; caf?", rec.Header().Get("X-Triage-Route-Reason"))
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))
	body := decodeBody(t, rec)
	assert.Equal(t, "chat.completion", body["object"])
	assert.Equal(t, "qwen-up", body["model"], "the model the upstream reported, not the route")
	assert.Equal(t, "chatcmpl-dec-1", body["id"])
	choice := body["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "hello", choice["message"].(map[string]any)["content"])
	assert.Equal(t, "stop", choice["finish_reason"])
	assert.Equal(t, map[string]any{
		"prompt_tokens": 5.0, "completion_tokens": 2.0, "total_tokens": 7.0,
		"prompt_tokens_details": map[string]any{"cached_tokens": 1.0},
	}, body["usage"])
}

func TestChat_RouteReasonHeader_OffByDefault(t *testing.T) {
	t.Parallel()
	rec := do(t, newServer(t, newFakeRouter()).API(), http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	assert.Empty(t, rec.Header().Get("X-Triage-Route-Reason"))
}

func TestChat_UnmaskHeader(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		unmask decision.UnmaskStatus
		want   string
	}{{decision.UnmaskPartial, "partial"}, {decision.UnmaskOK, ""}, {"", ""}} {
		fr := newFakeRouter()
		fr.chat = func(chat.Request) (route.Outcome, error) {
			out := okOutcome()
			out.Decision.Unmask = tc.unmask
			return out, nil
		}
		rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions",
			`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, tc.want, rec.Header().Get("X-Triage-Unmask"), string(tc.unmask))
	}
}

func TestChat_ToolCallResponse_ContentNull(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.chat = func(chat.Request) (route.Outcome, error) {
		out := okOutcome()
		out.Model = ""
		out.Response.Choices = []chat.Choice{{
			Message: chat.Message{
				Role:      chat.RoleAssistant,
				ToolCalls: []chat.ToolCall{{ID: "c1", Function: chat.FunctionCall{Name: "f", Arguments: `{"a":1}`}}},
			},
			FinishReason: chat.FinishToolCalls,
		}}
		out.Response.ID, out.Response.Created, out.Response.SystemFingerprint = "up-1", 42, "fp"
		return out, nil
	}
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)

	body := decodeBody(t, rec)
	msg := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	assert.Nil(t, msg["content"])
	call := msg["tool_calls"].([]any)[0].(map[string]any)
	assert.Equal(t, "function", call["type"])
	assert.Equal(t, map[string]any{"name": "f", "arguments": `{"a":1}`}, call["function"])
	assert.Equal(t, "up-1", body["id"])
	assert.InDelta(t, 42.0, body["created"], 0)
	assert.Equal(t, "fp", body["system_fingerprint"])
	assert.Equal(t, "ignored", body["model"], "falls back to the response model")
}

func TestChat_DecodesRequest(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	h := newServer(t, fr).API()
	body := `{"model":"auto","stream":false,"temperature":0.5,"top_p":0.9,"max_tokens":10,
	  "max_completion_tokens":20,"seed":7,"user":"u1","stop":"END","tool_choice":"auto",
	  "response_format":{"type":"json_object"},"logit_bias":{"1":2},"n":2,
	  "stream_options":{"include_usage":true},
	  "tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"},"strict":true}}],
	  "messages":[
	    {"role":"system","content":"s"},
	    {"role":"user","content":[{"type":"text","text":"a"},
	      {"type":"image_url","image_url":{"url":"http://x/i.png","detail":"low"}}]},
	    {"role":"assistant","content":null,"tool_calls":[
	      {"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
	    {"role":"tool","tool_call_id":"c1","name":"f","content":"result"}]}`

	rec := do(t, h, http.MethodPost, "/v1/chat/completions", body,
		"Authorization", "Bearer secret", "Cookie", "c=1", "X-Triage-Privacy", "local_only",
		"X-Fake-Scenario", "echo", "Content-Type", "application/json", "Traceparent", "00-aa-bb-01")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := fr.last(t)
	assert.Equal(t, "auto", got.Model)
	assert.Equal(t, "id-2", got.ID, "the decision id, id-1 being the request id")
	assert.InDelta(t, 0.5, *got.Temperature, 0)
	assert.InDelta(t, 0.9, *got.TopP, 0)
	assert.Equal(t, 10, *got.MaxTokens)
	assert.Equal(t, 20, *got.MaxCompletionTokens)
	assert.Equal(t, int64(7), *got.Seed)
	assert.Equal(t, "u1", got.User)
	assert.Equal(t, []string{"END"}, got.Stop)
	assert.JSONEq(t, `"auto"`, string(got.ToolChoice))
	assert.JSONEq(t, `{"type":"json_object"}`, string(got.ResponseFormat))
	assert.True(t, got.IncludeUsage)
	assert.False(t, got.Stream)
	require.Len(t, got.Extra, 2)
	assert.JSONEq(t, `2`, string(got.Extra["n"]))
	assert.JSONEq(t, `{"1":2}`, string(got.Extra["logit_bias"]))
	assert.Equal(t, map[string]string{"X-Triage-Privacy": "local_only", "X-Fake-Scenario": "echo"}, got.Headers)

	require.Len(t, got.Messages, 4)
	assert.Equal(t, chat.TextMessage(chat.RoleSystem, "s"), got.Messages[0])
	assert.False(t, got.Messages[1].ContentIsString)
	assert.Equal(t, []chat.Part{
		{Type: chat.PartText, Text: "a"},
		{Type: chat.PartImage, ImageURL: "http://x/i.png", ImageDetail: "low"},
	}, got.Messages[1].Content)
	assert.Empty(t, got.Messages[2].Content)
	assert.Equal(t, []chat.ToolCall{{ID: "c1", Type: "function", Function: chat.FunctionCall{Name: "f", Arguments: "{}"}}},
		got.Messages[2].ToolCalls)
	assert.Equal(t, "c1", got.Messages[3].ToolCallID)
	assert.Equal(t, "f", got.Messages[3].Name)
	strict := true
	assert.Equal(t, []chat.Tool{{Type: "function", Function: chat.ToolFunction{
		Name: "f", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict,
	}}}, got.Tools)
}

func TestChat_StopArray_AndNullFields(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	h := newServer(t, fr).API()
	do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","stop":["a","b"],"tool_choice":null,"response_format":null,
		"messages":[{"role":"user","content":"x"}]}`)
	got := fr.last(t)
	assert.Equal(t, []string{"a", "b"}, got.Stop)
	assert.Nil(t, got.ToolChoice)
	assert.Nil(t, got.ResponseFormat)
	assert.Nil(t, got.Extra)
	assert.Nil(t, got.Headers)

	do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","stop":null,"messages":[{"role":"user","content":"x"}]}`)
	assert.Nil(t, fr.last(t).Stop)
}

func TestChat_InvalidRequests_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, param string
	}{
		{"empty body", ``, ""},
		{"not json", `{`, ""},
		{"not an object", `[]`, ""},
		{"missing model", `{"messages":[{"role":"user","content":"x"}]}`, "model"},
		{"missing messages", `{"model":"auto"}`, "messages"},
		{"empty messages", `{"model":"auto","messages":[]}`, "messages"},
		{"bad role", `{"model":"auto","messages":[{"role":"robot","content":"x"}]}`, "messages[0]"},
		{"bad content type", `{"model":"auto","messages":[{"role":"user","content":5}]}`, "messages[0]"},
		{"bad part type", `{"model":"auto","messages":[{"role":"user","content":[{"type":"audio"}]}]}`, "messages[0]"},
		{"bad stop", `{"model":"auto","stop":5,"messages":[{"role":"user","content":"x"}]}`, "stop"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fr := newFakeRouter()
			rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions", tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errField(t, rec, "code"))
			assert.Equal(t, "invalid_request_error", errField(t, rec, "type"))
			assert.NotEmpty(t, errField(t, rec, "message"))
			if tc.param == "" {
				assert.Nil(t, errField(t, rec, "param"))
			} else {
				assert.Equal(t, tc.param, errField(t, rec, "param"))
			}
			assert.Empty(t, fr.reqs, "the router is never called")
		})
	}
}

func TestChat_BodyTooLarge_413(t *testing.T) {
	t.Parallel()
	h := newServer(t, newFakeRouter(), httpapi.WithSettings(func() httpapi.Settings {
		return httpapi.Settings{MaxBodyBytes: 64}
	})).API()
	big := `{"model":"auto","messages":[{"role":"user","content":"` + strings.Repeat("x", 200) + `"}]}`

	for _, path := range []string{"/v1/chat/completions", "/v1/embeddings"} {
		rec := do(t, h, http.MethodPost, path, big)

		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Equal(t, "request_too_large", errField(t, rec, "code"))
	}
}

func TestChat_ErrorMapping_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		typ    string
	}{
		{"unknown route", fmt.Errorf("x: %w", route.ErrUnknownRoute), 404, "model_not_found", "invalid_request_error"},
		{"scan limit", privacy.ErrScanLimit, 413, "privacy_scan_limit", "invalid_request_error"},
		{"mode", privacy.ErrModeNotAllowed, 400, "privacy_mode_not_allowed", "invalid_request_error"},
		{"no local", privacy.ErrNoLocalTarget, 503, "privacy_no_local_target", "server_error"},
		{"no target", route.ErrNoEligibleTarget, 503, "no_eligible_target", "server_error"},
		{"rate limited", provider.ErrRateLimited, 429, "rate_limit_exceeded", "rate_limit_error"},
		{"timeout", provider.ErrTimeout, 504, "upstream_timeout", "server_error"},
		{"unavailable", provider.ErrUpstreamUnavailable, 502, "upstream_unavailable", "server_error"},
		{"bad request", provider.ErrBadRequest, 400, "upstream_bad_request", "invalid_request_error"},
		{"context length", provider.ErrContextLength, 400, "context_length_exceeded", "invalid_request_error"},
		{"internal", errors.New("db password is hunter2"), 500, "internal_error", "server_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fr := newFakeRouter()
			fr.chat = func(chat.Request) (route.Outcome, error) {
				return route.Outcome{DecisionID: "dec-9", Route: "auto"}, tc.err
			}
			rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions",
				`{"model":"auto","messages":[{"role":"user","content":"x"}]}`)

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, tc.code, errField(t, rec, "code"))
			assert.Equal(t, tc.typ, errField(t, rec, "type"))
			assert.Equal(t, "dec-9", rec.Header().Get("X-Triage-Decision"))
			assert.NotContains(t, rec.Body.String(), "hunter2")
		})
	}
}

func TestChat_UnknownRoute_ParamModel(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.chat = func(chat.Request) (route.Outcome, error) { return route.Outcome{}, route.ErrUnknownRoute }
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions",
		`{"model":"nope","messages":[{"role":"user","content":"x"}]}`)

	assert.Equal(t, "model", errField(t, rec, "param"))
	assert.Empty(t, rec.Header().Get("X-Triage-Decision"))
}

func TestChat_LongErrorMessage_Truncated(t *testing.T) {
	t.Parallel()
	fr := newFakeRouter()
	fr.chat = func(chat.Request) (route.Outcome, error) {
		return route.Outcome{}, fmt.Errorf("%s: %w", strings.Repeat("x", 5000), provider.ErrBadRequest)
	}
	rec := do(t, newServer(t, fr).API(), http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"x"}]}`)

	assert.LessOrEqual(t, len(errField(t, rec, "message").(string)), 1000)
}
