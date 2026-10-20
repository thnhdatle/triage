// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServe_Chat_HeadersDecisionAndMetrics(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))

	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "hello there", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	resp := decode[chatResponse](t, r.body)
	assert.Equal(t, "fake-small", resp.Model, "response model is the upstream model (F1)")
	require.Len(t, resp.Choices, 1)
	assert.NotEmpty(t, resp.Choices[0].Message.Content)
	assert.Equal(t, "triage-auto", r.header.Get("X-Triage-Route"))
	assert.Equal(t, "local-small", r.header.Get("X-Triage-Target"))
	assert.Contains(t, r.header.Get("X-Triage-Route-Reason"), "strategy:length")
	id := r.header.Get("X-Triage-Decision")
	require.Len(t, id, 26)

	d := tr.decision(t, id)
	assert.Equal(t, "triage-auto", d.Route)
	assert.Equal(t, "length", d.Strategy)
	assert.Equal(t, "weak", d.Tier)
	require.Len(t, d.Attempts, 1)
	assert.Equal(t, "ok", d.Attempts[0].Status)

	m := tr.metrics(t)
	assert.Contains(t, m, `triage_tier_decisions_total{route="triage-auto",tier="weak"} 1`)
	assert.Contains(t, m,
		`triage_upstream_request_duration_seconds_count{model="fake-small",provider="fake",status="ok"} 1`)
	assert.Contains(t, m, `triage_strategy_score_count{strategy="length"} 1`)
	assert.Contains(t, m, `triage_target_healthy{target="local-small"} 1`)
}

func TestServe_Chat_Random_And_UnknownModel(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))
	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-random", "hi", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "random", tr.decision(t, r.header.Get("X-Triage-Decision")).Strategy)

	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("nope", "hi", false))
	assert.Equal(t, http.StatusNotFound, r.status)
	assert.Contains(t, string(r.body), "model_not_found")
}

func TestServe_Stream_ChunksAndDone(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))
	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions",
		chatBody("strong-masked", "stream this text back to me please", true))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "text/event-stream", strings.Split(r.header.Get("Content-Type"), ";")[0])
	assert.Equal(t, "cloud-strong", r.header.Get("X-Triage-Target"))
	data := sseEvents(r.body)
	require.GreaterOrEqual(t, len(data), 3)
	assert.Equal(t, "[DONE]", data[len(data)-1])
	assert.Equal(t, "stream this text back to me please", streamText(t, data))
}

func TestServe_Fallback_NonStreamAndMidStreamContinuation(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))

	f.script(t, `[{"model":"fake-small","scenario":"500","count":1}]`)
	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "fall back please", false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "cloud-strong", r.header.Get("X-Triage-Target"))
	assert.Equal(t, "fall back please", decode[chatResponse](t, r.body).Choices[0].Message.Content)
	d := tr.decision(t, r.header.Get("X-Triage-Decision"))
	require.Len(t, d.Attempts, 2)
	assert.Equal(t, "local-small", d.Attempts[0].Target)
	assert.NotEqual(t, "ok", d.Attempts[0].Status)
	assert.Equal(t, "cloud-strong", d.Attempts[1].Target)
	assert.Contains(t, tr.metrics(t), `triage_fallback_attempts_total{reason="`)

	f.script(t, `[{"model":"fake-small","scenario":"cut-stream","count":1,
		"params":{"cut_after":2,"chunk_bytes":4,"answer":"0123456789abcdefghij"}}]`)
	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "continue me", true))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "local-small", r.header.Get("X-Triage-Target"), "first relayed attempt names the target (F7)")
	data := sseEvents(r.body)
	assert.Equal(t, "[DONE]", data[len(data)-1])
	assert.True(t, strings.HasPrefix(streamText(t, data), "01234567"))
	d = tr.decision(t, r.header.Get("X-Triage-Decision"))
	require.Len(t, d.Attempts, 2)
	assert.True(t, d.Attempts[1].Continuation)
	assert.Equal(t, "cloud", d.Attempts[1].Locality)
}

func TestServe_Privacy_MaskRoundTripAndLocalOnly(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, strings.Replace(defaultRoutes,
		"    privacy: { mode: local_only }\n    default_tier: strong",
		"    privacy: { mode: local_only }\n    default_tier: strong\n    on_tier_unavailable: degrade", 1)))
	const secret = "my email is jo.doe@example.com and card 4111 1111 1111 1111, thanks"

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
	for _, rec := range f.requests(t) {
		if rec.Model == "fake-strong" {
			assert.NotContains(t, rec.Body, "jo.doe@example.com")
			assert.NotContains(t, rec.Body, "4111 1111 1111 1111")
			assert.Contains(t, rec.Body, "[[EMAIL_1]]")
		}
	}

	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("private", secret, false))
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "local-small", r.header.Get("X-Triage-Target"))
	assert.Contains(t, tr.decision(t, r.header.Get("X-Triage-Decision")).Reasons, "privacy:local_only")

	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", secret, false),
		"X-Triage-Privacy", "local_only")
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	assert.Equal(t, "local_only", tr.decision(t, r.header.Get("X-Triage-Decision")).Privacy)

	m := tr.metrics(t)
	assert.Contains(t, m, `triage_privacy_findings_total{route="strong-masked",type="email"} 2`)
}

func TestServe_Embeddings(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))
	r := tr.call(t, http.MethodPost, tr.data+"/v1/embeddings",
		map[string]any{"model": "embed", "input": []string{"a", "b"}})
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	out := decode[struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}](t, r.body)
	require.Len(t, out.Data, 2)
	assert.NotEmpty(t, out.Data[0].Embedding)
	assert.Equal(t, "local-embed", r.header.Get("X-Triage-Target"))
}

func TestServe_Auth(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	cfg := strings.Replace(baseConfig(f, defaultRoutes), "auth: { enabled: false }",
		"auth: { enabled: true, keys_env: TRIAGE_TEST_KEYS }", 1)
	tr := startTriage(t, cfg)
	r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "hi", false))
	assert.Equal(t, http.StatusUnauthorized, r.status)
	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "hi", false),
		"Authorization", "Bearer wrong")
	assert.Equal(t, http.StatusUnauthorized, r.status)
	r = tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "hi", false),
		"Authorization", "Bearer k2")
	assert.Equal(t, http.StatusOK, r.status, string(r.body))
}

func TestServe_AdminAPI(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))
	for range 3 {
		r := tr.call(t, http.MethodPost, tr.data+"/v1/chat/completions", chatBody("triage-auto", "hi", false))
		require.Equal(t, http.StatusOK, r.status)
	}
	r := tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions?limit=2", nil)
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	page := decode[struct {
		Data       []decisionView `json:"data"`
		NextCursor string         `json:"next_cursor"`
	}](t, r.body)
	require.Len(t, page.Data, 2)
	require.NotEmpty(t, page.NextCursor)
	r = tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions?limit=2&cursor="+page.NextCursor, nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Len(t, decode[struct {
		Data []decisionView `json:"data"`
	}](t, r.body).Data, 1)
	assert.Equal(t, http.StatusNotFound, tr.call(t, http.MethodGet, tr.admin+"/v1/routing-decisions/nope", nil).status)

	r = tr.call(t, http.MethodGet, tr.admin+"/v1/admin/config", nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Contains(t, string(r.body), "[redacted]")
	assert.NotContains(t, string(r.body), `"echo"`)

	r = tr.call(t, http.MethodGet, tr.admin+"/v1/admin/models", nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Contains(t, string(r.body), `"healthy":true`)

	r = tr.call(t, http.MethodGet, tr.data+"/v1/models", nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Contains(t, string(r.body), "triage-auto")
	assert.Contains(t, string(r.body), "cloud-strong", "api.expose_targets lists targets")

	for _, p := range []string{"/healthz", "/readyz", "/v1/version"} {
		assert.Equal(t, http.StatusOK, tr.call(t, http.MethodGet, tr.admin+p, nil).status, p)
		assert.Equal(t, http.StatusOK, tr.call(t, http.MethodGet, tr.data+p, nil).status, p)
	}
	assert.Equal(t, http.StatusNotFound, tr.call(t, http.MethodGet, tr.data+"/v1/routing-decisions", nil).status,
		"decisions are on the admin listener only")
}
