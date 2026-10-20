// SPDX-License-Identifier: Apache-2.0

package fakellm_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/fakellm"
)

func TestServer_PutScript_AppliesCountsInOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	script := `[{"model":"m","scenario":"500","count":2},{"model":"m","scenario":"echo","count":1},
		{"model":"m","scenario":"ok","count":1,"params":{"answer":"scripted {n}"}},
		{"model":"other","scenario":"429","count":1}]`
	require.Equal(t, http.StatusNoContent, e.do(http.MethodPut, "/_fake/script", script, nil).status)

	assert.Equal(t, http.StatusInternalServerError, e.chat(userBody("m", "x", ""), nil).status)
	assert.Equal(t, http.StatusInternalServerError, e.chat(userBody("m", "x", ""), nil).status)
	assert.Equal(t, "echoed", *decodeCompletion(t, e.chat(userBody("m", "echoed", ""), nil)).Choices[0].Message.Content)
	assert.Equal(t, "scripted 4", *decodeCompletion(t, e.chat(userBody("m", "x", ""), nil)).Choices[0].Message.Content)
	// Script exhausted: back to ok.
	assert.NotEqual(t, "scripted 5", *decodeCompletion(t, e.chat(userBody("m", "x", ""), nil)).Choices[0].Message.Content)
	assert.Equal(t, http.StatusTooManyRequests, e.chat(userBody("other", "x", ""), nil).status)
	assert.Equal(t, http.StatusOK, e.chat(userBody("other", "x", ""), nil).status)
}

func TestServer_PutScript_HeaderTakesPrecedence(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.do(http.MethodPut, "/_fake/script", `[{"model":"m","scenario":"500","count":1}]`, nil)
	r := e.chat(userBody("m", "x", ""), map[string]string{"X-Fake-Scenario": "echo"})
	assert.Equal(t, "x", *decodeCompletion(t, r).Choices[0].Message.Content)
	assert.Equal(t, http.StatusInternalServerError, e.chat(userBody("m", "x", ""), nil).status,
		"header request does not consume the rule")
}

func TestServer_PutScript_Params(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.do(http.MethodPut, "/_fake/script",
		`[{"model":"m","scenario":"ok","count":1,"params":{"answer":"abcdef","chunk_bytes":2}},
		  {"model":"m","scenario":"cut-stream","count":1,"params":{"cut_after":2,"chunk_bytes":2,"answer":"abcdef"}}]`, nil)
	chunks := decodeStream(t, e.chat(userBody("m", "x", `,"stream":true`), nil))
	assert.Len(t, chunks, 4, "3 content chunks and the finish chunk")
	_, err := e.try(http.MethodPost, "/v1/chat/completions", userBody("m", "x", `,"stream":true`), nil)
	require.Error(t, err)
}

func TestServer_PutScript_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, body string }{
		{"not json", `x`},
		{"no model", `[{"scenario":"ok","count":1}]`},
		{"bad scenario", `[{"model":"m","scenario":"zzz","count":1}]`},
		{"zero count", `[{"model":"m","scenario":"ok","count":0}]`},
		{"unknown field", `[{"model":"m","scenario":"ok","count":1,"extra":1}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			assert.Equal(t, http.StatusBadRequest, e.do(http.MethodPut, "/_fake/script", tc.body, nil).status)
		})
	}
}

func TestServer_DeleteScript_Clears(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.do(http.MethodPut, "/_fake/script", `[{"model":"m","scenario":"500","count":5}]`, nil)
	e.do(http.MethodDelete, "/_fake/script", "", nil)
	assert.Equal(t, http.StatusOK, e.chat(userBody("m", "x", ""), nil).status)
}

type logResp struct {
	Requests []struct {
		Method     string              `json:"method"`
		Path       string              `json:"path"`
		Headers    map[string][]string `json:"headers"`
		Body       string              `json:"body"`
		Model      string              `json:"model"`
		Kind       string              `json:"kind"`
		InputCount int                 `json:"input_count"`
	} `json:"requests"`
	Dropped int `json:"dropped"`
}

func getLog(t *testing.T, e *env) logResp {
	t.Helper()
	r := e.do(http.MethodGet, "/_fake/requests", "", nil)
	require.Equal(t, http.StatusOK, r.status)
	var l logResp
	require.NoError(t, json.Unmarshal([]byte(r.body), &l))
	require.NotNil(t, l.Requests)
	return l
}

func TestServer_GetRequests_RecordsFields(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := userBody("m", "x", "")
	e.chat(body, map[string]string{"X-Custom": "v1"})
	embBody := `{"model":"em","input":["a","b","c"]}`
	e.do(http.MethodPost, "/v1/embeddings", embBody, nil)
	e.do(http.MethodGet, "/v1/models", "", nil)

	l := getLog(t, e)
	require.Len(t, l.Requests, 2)
	assert.Equal(t, 0, l.Dropped)
	c := l.Requests[0]
	assert.Equal(t, "POST", c.Method)
	assert.Equal(t, "/v1/chat/completions", c.Path)
	assert.Equal(t, body, c.Body)
	assert.Equal(t, "m", c.Model)
	assert.Equal(t, "chat", c.Kind)
	assert.Equal(t, []string{"v1"}, c.Headers["X-Custom"])
	em := l.Requests[1]
	assert.Equal(t, "embeddings", em.Kind)
	assert.Equal(t, 3, em.InputCount)
	assert.Equal(t, embBody, em.Body)
	assert.Equal(t, "em", em.Model)
}

func TestServer_GetRequests_RingBufferDropped(t *testing.T) {
	t.Parallel()
	e := newEnv(t, fakellm.WithRecord(true, 3))
	for i := range 5 {
		e.chat(userBody("m", fmt.Sprintf("req%d", i), ""), nil)
	}
	l := getLog(t, e)
	require.Len(t, l.Requests, 3)
	assert.Equal(t, 2, l.Dropped)
	for i, r := range l.Requests {
		assert.Contains(t, r.Body, fmt.Sprintf("req%d", i+2), "oldest evicted, order kept")
	}
}

func TestServer_DeleteRequests_Clears(t *testing.T) {
	t.Parallel()
	e := newEnv(t, fakellm.WithRecord(true, 1))
	e.chat(userBody("m", "a", ""), nil)
	e.chat(userBody("m", "b", ""), nil)
	assert.Equal(t, 1, getLog(t, e).Dropped)
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/_fake/requests", "", nil).status)
	l := getLog(t, e)
	assert.Empty(t, l.Requests)
	assert.Zero(t, l.Dropped)
}

func TestServer_GetRequests_RecordDisabled(t *testing.T) {
	t.Parallel()
	e := newEnv(t, fakellm.WithRecord(false, 0))
	e.chat(userBody("m", "a", ""), nil)
	l := getLog(t, e)
	assert.Empty(t, l.Requests)
	assert.Zero(t, l.Dropped)
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/_fake/requests", "", nil).status)
}

func TestServer_GetRequests_RecordsBadRequests(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.chat(`{`, nil)
	e.do(http.MethodPost, "/v1/embeddings", `{`, nil)
	e.do(http.MethodPost, "/v1/embeddings", `{"model":"m"}`, nil)
	assert.Len(t, getLog(t, e).Requests, 3)
}

type embResp struct {
	Object string `json:"object"`
	Model  string `json:"model"`
	Data   []struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func embed(t *testing.T, e *env, body string) embResp {
	t.Helper()
	r := e.do(http.MethodPost, "/v1/embeddings", body, nil)
	require.Equal(t, http.StatusOK, r.status, r.body)
	var out embResp
	require.NoError(t, json.Unmarshal([]byte(r.body), &out))
	return out
}

func TestServer_Embeddings_DeterministicAndNormalised(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := embed(t, e, `{"model":"m1","input":"hello world"}`)
	b := embed(t, e, `{"model":"m1","input":["hello world","other text here"]}`)
	c := embed(t, e, `{"model":"m2","input":"hello world"}`)

	assert.Equal(t, "list", a.Object)
	assert.Equal(t, "m1", a.Model)
	require.Len(t, a.Data, 1)
	require.Len(t, b.Data, 2)
	assert.Len(t, a.Data[0].Embedding, 1024, "default dimension")
	assert.Equal(t, a.Data[0].Embedding, b.Data[0].Embedding, "same text, same vector")
	assert.NotEqual(t, a.Data[0].Embedding, b.Data[1].Embedding)
	assert.NotEqual(t, a.Data[0].Embedding, c.Data[0].Embedding, "model changes the space")
	for _, d := range append(b.Data, c.Data...) {
		assert.Equal(t, "embedding", d.Object)
		var sum float64
		for _, v := range d.Embedding {
			sum += v * v
		}
		assert.InDelta(t, 1.0, math.Sqrt(sum), 1e-4)
	}
	assert.Equal(t, 1, b.Data[1].Index)
	assert.Equal(t, 5, b.Usage.PromptTokens)
	assert.Equal(t, 5, b.Usage.TotalTokens)
}

func TestServer_Embeddings_Dimensions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, dim := range []int{1, 7, 8, 9, 100, 2048} {
		out := embed(t, e, fmt.Sprintf(`{"model":"m","input":"x","dimensions":%d}`, dim))
		assert.Len(t, out.Data[0].Embedding, dim)
	}
}

func TestServer_Embeddings_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, body string }{
		{"bad json", `{`},
		{"missing input", `{"model":"m"}`},
		{"number input", `{"model":"m","input":5}`},
		{"empty array", `{"model":"m","input":[]}`},
		{"zero dims", `{"model":"m","input":"x","dimensions":0}`},
		{"huge dims", `{"model":"m","input":"x","dimensions":99999999}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			assert.Equal(t, http.StatusBadRequest, e.do(http.MethodPost, "/v1/embeddings", tc.body, nil).status)
		})
	}
}

func TestServer_GetModels_ListsSeen(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := func() []string {
		r := e.do(http.MethodGet, "/v1/models", "", nil)
		var out struct {
			Object string `json:"object"`
			Data   []struct {
				ID     string `json:"id"`
				Object string `json:"object"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.body), &out))
		assert.Equal(t, "list", out.Object)
		got := make([]string, 0, len(out.Data))
		for _, d := range out.Data {
			assert.Equal(t, "model", d.Object)
			got = append(got, d.ID)
		}
		return got
	}
	assert.Equal(t, []string{"fake-default"}, ids())
	e.chat(userBody("zeta", "x", ""), nil)
	e.do(http.MethodPost, "/v1/embeddings", `{"model":"alpha","input":"x"}`, nil)
	e.do(http.MethodPut, "/_fake/script", `[{"model":"scripted","scenario":"ok","count":1}]`, nil)
	assert.Equal(t, []string{"fake-default", "alpha", "scripted", "zeta"}, ids())
}

func TestServer_Health_OK(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		assert.Equal(t, http.StatusOK, e.do(http.MethodGet, p, "", nil).status, p)
	}
	assert.Equal(t, http.StatusMethodNotAllowed, e.do(http.MethodGet, "/v1/chat/completions", "", nil).status)
}

func TestServer_New_Validation(t *testing.T) {
	t.Parallel()
	_, err := fakellm.New(fakellm.WithRecord(true, 0))
	require.Error(t, err)
	_, err = fakellm.New(fakellm.WithClock(nil))
	require.Error(t, err)
	_, err = fakellm.New(fakellm.WithRecord(false, -5))
	require.NoError(t, err)
}

func TestServer_Chat_BodyTooLarge(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	big := strings.Repeat("a", 33<<20)
	r, err := e.try(http.MethodPost, "/v1/chat/completions", big, nil)
	if err == nil {
		assert.Equal(t, http.StatusBadRequest, r.status)
	}
}
