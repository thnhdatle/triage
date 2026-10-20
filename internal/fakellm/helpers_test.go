// SPDX-License-Identifier: Apache-2.0

package fakellm_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/fakellm"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type env struct {
	t      *testing.T
	ts     *httptest.Server
	client *http.Client
	clk    *clock.Fake
}

func newEnv(t *testing.T, opts ...fakellm.Option) *env {
	t.Helper()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	srv, err := fakellm.New(append([]fakellm.Option{fakellm.WithClock(clk)}, opts...)...)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	tr := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		ts.Close()
	})
	return &env{t: t, ts: ts, client: &http.Client{Transport: tr}, clk: clk}
}

type reply struct {
	status int
	header http.Header
	body   string
}

func (e *env) do(method, path, body string, hdr map[string]string) reply {
	e.t.Helper()
	rep, err := e.try(method, path, body, hdr)
	require.NoError(e.t, err)
	return rep
}

func (e *env) try(method, path, body string, hdr map[string]string) (reply, error) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.ts.URL+path, strings.NewReader(body))
	require.NoError(e.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: string(b)}, err
}

func (e *env) chat(body string, hdr map[string]string) reply {
	e.t.Helper()
	return e.do(http.MethodPost, "/v1/chat/completions", body, hdr)
}

func userBody(model, text string, extra string) string {
	b, _ := json.Marshal(text)
	return `{"model":"` + model + `","messages":[{"role":"user","content":` + string(b) + `}]` + extra + `}`
}

type completion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func decodeCompletion(t *testing.T, r reply) completion {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, r.body)
	var c completion
	require.NoError(t, json.Unmarshal([]byte(r.body), &c))
	require.Len(t, c.Choices, 1)
	return c
}

type streamChunk struct {
	Model   string `json:"model"`
	Object  string `json:"object"`
	Choices []struct {
		Delta struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// sseLines returns the payloads of the data: lines.
func sseLines(body string) []string {
	var out []string
	for _, ev := range strings.Split(body, "\n\n") {
		if p, ok := strings.CutPrefix(ev, "data: "); ok {
			out = append(out, p)
		}
	}
	return out
}

func decodeStream(t *testing.T, r reply) []streamChunk {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, r.body)
	lines := sseLines(r.body)
	require.NotEmpty(t, lines)
	require.Equal(t, "[DONE]", lines[len(lines)-1])
	out := make([]streamChunk, 0, len(lines))
	for _, l := range lines[:len(lines)-1] {
		var c streamChunk
		require.NoError(t, json.Unmarshal([]byte(l), &c), l)
		out = append(out, c)
	}
	return out
}

func streamText(chunks []streamChunk) string {
	var b strings.Builder
	for _, c := range chunks {
		if len(c.Choices) > 0 && c.Choices[0].Delta.Content != nil {
			b.WriteString(*c.Choices[0].Delta.Content)
		}
	}
	return b.String()
}
