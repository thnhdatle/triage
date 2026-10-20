// SPDX-License-Identifier: Apache-2.0

//go:build integration

package wire_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/dispatch"
)

// The triage server runs in-process (wire.Serve) against the fakellm
// container; every upstream request crosses the Docker network.

// itConfig is a two-target configuration over the fakellm container. Upstream
// model names start with prefix so the shared request log can be filtered per
// test; smallHeaders are the extra_headers of the local weak target (a
// fakellm scenario), the cloud strong target always echoes.
func itConfig(f *fake, prefix, smallHeaders string) string {
	if smallHeaders == "" {
		smallHeaders = "{}"
	}
	return `
server: { listen: "127.0.0.1:0" }
admin: { listen: "127.0.0.1:0" }
auth: { enabled: false }
api: { expose_decision_detail: true, expose_targets: true }
providers:
  fake: { type: openaicompat, base_url: "` + f.baseURL() + `", api_key_env: FAKE_KEY }
health: { interval: 1h }
targets:
  - { name: local-small, provider: fake, model: ` + prefix + `-small, locality: local, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode], extra_headers: ` + smallHeaders + `,
      pricing: { input_per_mtok: 0, output_per_mtok: 0 } }
  - { name: cloud-strong, provider: fake, model: ` + prefix + `-strong, locality: cloud, tier: strong,
      context_window: 200000, capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: echo },
      pricing: { input_per_mtok: 1.25, output_per_mtok: 10.0 } }
  - { name: local-embed, provider: fake, model: ` + prefix + `-embed, locality: local, capabilities: [embeddings] }
dispatch: { attempt_timeout: 5s, total_timeout: 20s, backoff: { base: 1ms, max: 5ms } }
routes:
  - name: weak-first
    pool: [local-small, cloud-strong]
    privacy: { mode: mask }
    default_tier: weak
    fallback: [tier:strong]
  - name: strong-masked
    pool: [local-small, cloud-strong]
    privacy: { mode: mask }
    default_tier: strong
  - name: embed
    pool: [local-embed]
    privacy: { mode: mask }
`
}

// upstream returns the fakellm log entries for model, asserting nothing was
// dropped.
func upstream(t *testing.T, f *fake, model string) []fakeRecord {
	t.Helper()
	var out []fakeRecord
	for _, r := range f.requests(t) {
		if r.Model == model {
			out = append(out, r)
		}
	}
	return out
}

type upstreamBody struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

func TestIntegration_Serve_ChatAndStream(t *testing.T) {
	t.Parallel()
	f := containerFake(t)
	tr := startTriage(t, itConfig(f, "it-chat", ""))

	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("weak-first", "hello there", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	resp := decode[chatResponse](t, r.body)
	assert.Equal(t, "it-chat-small", resp.Model)
	require.Len(t, resp.Choices, 1)
	assert.Len(t, strings.Fields(resp.Choices[0].Message.Content), 6, "fakellm ok scenario: six words")
	assert.Equal(t, "local-small", r.header.Get("X-Triage-Target"))
	d := tr.decision(t, r.header.Get("X-Triage-Decision"))
	require.Len(t, d.Attempts, 1)
	assert.Equal(t, "ok", d.Attempts[0].Status)

	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions",
		chatBody("strong-masked", "stream this text back to me please", true))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "text/event-stream", strings.Split(r.header.Get("Content-Type"), ";")[0])
	assert.Equal(t, "cloud-strong", r.header.Get("X-Triage-Target"))
	data := sseEvents(r.body)
	require.GreaterOrEqual(t, len(data), 3)
	assert.Equal(t, "[DONE]", data[len(data)-1])
	assert.Equal(t, "stream this text back to me please", streamText(t, data))

	small := upstream(t, f, "it-chat-small")
	require.Len(t, small, 1)
	assert.Equal(t, "Bearer sk-test", small[0].Headers.Get("Authorization"))
	strong := upstream(t, f, "it-chat-strong")
	require.Len(t, strong, 1)
	body := decode[upstreamBody](t, []byte(strong[0].Body))
	assert.True(t, body.Stream)
	require.NotNil(t, body.StreamOptions)
	assert.True(t, body.StreamOptions.IncludeUsage, "usage is always requested on streams")
	assert.Equal(t, "echo", strong[0].Headers.Get("X-Fake-Scenario"), "target extra_headers are sent")
}

func TestIntegration_Serve_Fallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		prefix   string
		headers  string
		scenario string
	}{
		{name: "500", prefix: "it-fb500", headers: `{ X-Fake-Scenario: "500" }`},
		{name: "429", prefix: "it-fb429", headers: `{ X-Fake-Scenario: "429" }`},
		{name: "connection reset", prefix: "it-fbcut", headers: `{ X-Fake-Scenario: cut-stream }`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := containerFake(t)
			tr := startTriage(t, itConfig(f, tc.prefix, tc.headers))
			r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions",
				chatBody("weak-first", "fall back please", false))
			require.Equal(t, http.StatusOK, r.status, string(r.body))
			assert.Equal(t, "cloud-strong", r.header.Get("X-Triage-Target"))
			assert.Equal(t, "fall back please", decode[chatResponse](t, r.body).Choices[0].Message.Content)
			d := tr.decision(t, r.header.Get("X-Triage-Decision"))
			require.Len(t, d.Attempts, 2)
			assert.Equal(t, "local-small", d.Attempts[0].Target)
			assert.NotEqual(t, "ok", d.Attempts[0].Status)
			assert.Equal(t, "cloud-strong", d.Attempts[1].Target)
			assert.Equal(t, "ok", d.Attempts[1].Status)
			assert.Contains(t, tr.metrics(t), `triage_fallback_attempts_total{reason="`)
			assert.NotEmpty(t, upstream(t, f, tc.prefix+"-small"))
			assert.Len(t, upstream(t, f, tc.prefix+"-strong"), 1)
		})
	}
}

func TestIntegration_Serve_MidStreamCutContinuation(t *testing.T) {
	t.Parallel()
	f := containerFake(t)
	tr := startTriage(t, itConfig(f, "it-cont", `{ X-Fake-Scenario: cut-stream, X-Fake-Cut-After: "2",
      X-Fake-Chunk-Bytes: "4", X-Fake-Answer: "0123456789abcdefghij" }`))

	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("weak-first", "continue me", true))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "local-small", r.header.Get("X-Triage-Target"), "first relayed attempt names the target (F7)")
	data := sseEvents(r.body)
	require.NotEmpty(t, data)
	assert.Equal(t, "[DONE]", data[len(data)-1])
	text := streamText(t, data)
	assert.True(t, strings.HasPrefix(text, "01234567"), text)
	assert.Equal(t, dispatch.ContinuationInstruction, strings.TrimPrefix(text, "01234567"),
		"the continuation attempt's output follows the relayed text")

	d := tr.decision(t, r.header.Get("X-Triage-Decision"))
	require.Len(t, d.Attempts, 2)
	assert.False(t, d.Attempts[0].Continuation)
	assert.True(t, d.Attempts[1].Continuation)
	assert.Equal(t, "cloud", d.Attempts[1].Locality)

	strong := upstream(t, f, "it-cont-strong")
	require.Len(t, strong, 1)
	body := decode[upstreamBody](t, []byte(strong[0].Body))
	require.GreaterOrEqual(t, len(body.Messages), 3)
	partial := body.Messages[len(body.Messages)-2]
	assert.Equal(t, "assistant", partial.Role)
	assert.Equal(t, "01234567", partial.Content, "the relayed text is sent as the partial assistant message")
	last := body.Messages[len(body.Messages)-1]
	assert.Equal(t, "user", last.Role)
	assert.Equal(t, dispatch.ContinuationInstruction, last.Content)
}

const (
	itEmail = "jo.doe@example.com"
	itCard  = "4111 1111 1111 1111"
)

func TestIntegration_Serve_PrivacyMaskingReachesNoUpstream(t *testing.T) {
	t.Parallel()
	f := containerFake(t)
	// The local target echoes the user text and is cut after 32 bytes, so the
	// relayed partial text (raw, from a local target) contains the e-mail; the
	// cloud continuation must re-mask it.
	tr := startTriage(t, itConfig(f, "it-priv", `{ X-Fake-Scenario: cut-stream, X-Fake-Cut-After: "2",
      X-Fake-Chunk-Bytes: "16", X-Fake-Answer: "{user}" }`))
	secret := itEmail + " is my email and my card is " + itCard + ", thanks"

	for _, stream := range []bool{false, true} {
		r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("strong-masked", secret, stream))
		require.Equal(t, http.StatusOK, r.status, string(r.body))
		got := ""
		if stream {
			got = streamText(t, sseEvents(r.body))
		} else {
			got = decode[chatResponse](t, r.body).Choices[0].Message.Content
		}
		assert.Equal(t, secret, got, "masked attempt output is restored byte for byte (stream=%v)", stream)
		d := tr.decision(t, r.header.Get("X-Triage-Decision"))
		assert.Equal(t, map[string]int{"email": 1, "credit_card": 1}, d.Findings)
		require.Len(t, d.Attempts, 1)
		assert.True(t, d.Attempts[0].Masked)
	}

	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("weak-first", secret, true))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	text := streamText(t, sseEvents(r.body))
	assert.True(t, strings.HasPrefix(text, secret[:32]), text)
	d := tr.decision(t, r.header.Get("X-Triage-Decision"))
	require.Len(t, d.Attempts, 2)
	assert.True(t, d.Attempts[1].Continuation)
	assert.True(t, d.Attempts[1].Masked, "the cloud continuation is masked")

	strong := upstream(t, f, "it-priv-strong")
	require.Len(t, strong, 3, "two strong-masked attempts and one continuation")
	for _, rec := range strong {
		raw, err := json.Marshal(rec)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), itEmail, "no raw e-mail reaches the cloud target")
		assert.NotContains(t, string(raw), itCard, "no raw card number reaches the cloud target")
		assert.NotContains(t, string(raw), "4111111111111111")
		assert.Contains(t, rec.Body, "[[EMAIL_1]]")
	}
	assert.Contains(t, strong[0].Body, "[[CREDIT_CARD_1]]")
	small := upstream(t, f, "it-priv-small")
	require.Len(t, small, 1, "only the weak-first attempt went to the local target")
}

func TestIntegration_Serve_EmbeddingsAndAdmin(t *testing.T) {
	t.Parallel()
	f := containerFake(t)
	tr := startTriage(t, itConfig(f, "it-admin", ""))

	r := tr.call(t, http.MethodPost, tr.data+"/v1/embeddings",
		map[string]any{"model": "embed", "input": []string{"a", "b"}})
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	emb := decode[struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}](t, r.body)
	require.Len(t, emb.Data, 2)
	assert.NotEmpty(t, emb.Data[0].Embedding)
	require.Len(t, upstream(t, f, "it-admin-embed"), 1)

	for range 3 {
		r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("weak-first", "hi", false))
		require.Equal(t, http.StatusOK, r.status, string(r.body))
	}
	r = tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions?limit=2", nil)
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	page := decode[struct {
		Data       []decisionView `json:"data"`
		NextCursor string         `json:"next_cursor"`
	}](t, r.body)
	require.Len(t, page.Data, 2)
	require.NotEmpty(t, page.NextCursor)
	r = tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions?limit=10&cursor="+page.NextCursor, nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Len(t, decode[struct {
		Data []decisionView `json:"data"`
	}](t, r.body).Data, 2, "three chats and one embedding request")

	r = tr.call(t, http.MethodGet, tr.admin+"/v1/admin/config", nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Contains(t, string(r.body), "[redacted]")
	assert.NotContains(t, string(r.body), "sk-test")

	r = tr.call(t, http.MethodGet, tr.admin+"/v1/admin/models", nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Contains(t, string(r.body), `"healthy":true`, "health probe GET /v1/models reaches the container")

	m := tr.metrics(t)
	assert.Contains(t, m,
		`triage_upstream_request_duration_seconds_count{model="it-admin-small",provider="fake",status="ok"} 3`)
	assert.Contains(t, m, `triage_target_healthy{target="local-small"} 1`)

	for _, p := range []string{"/healthz", "/readyz", "/v1/version"} {
		assert.Equal(t, http.StatusOK, tr.call(t, http.MethodGet, tr.admin+p, nil).status, p)
		assert.Equal(t, http.StatusOK, tr.call(t, http.MethodGet, tr.data+p, nil).status, p)
	}
}
