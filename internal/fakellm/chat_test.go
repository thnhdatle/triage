// SPDX-License-Identifier: Apache-2.0

package fakellm_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/testutil"
)

func TestServer_Chat_OKDeterministic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := decodeCompletion(t, e.chat(userBody("m1", "hello there world", ""), nil))
	b := decodeCompletion(t, e.chat(userBody("m1", "hello there world", ""), nil))
	c := decodeCompletion(t, e.chat(userBody("m2", "hello there world", ""), nil))
	d := decodeCompletion(t, e.chat(userBody("m1", "other", ""), nil))

	require.NotNil(t, a.Choices[0].Message.Content)
	assert.Equal(t, *a.Choices[0].Message.Content, *b.Choices[0].Message.Content)
	assert.NotEqual(t, *a.Choices[0].Message.Content, *c.Choices[0].Message.Content)
	assert.NotEqual(t, *a.Choices[0].Message.Content, *d.Choices[0].Message.Content)
	assert.Equal(t, "chat.completion", a.Object)
	assert.Equal(t, "m1", a.Model)
	assert.Equal(t, "assistant", a.Choices[0].Message.Role)
	assert.Equal(t, "stop", a.Choices[0].FinishReason)
	assert.Equal(t, int64(1_700_000_000), a.Created)
	assert.NotEmpty(t, a.ID)
	assert.Equal(t, 3, a.Usage.PromptTokens)
	assert.Equal(t, len(strings.Fields(*a.Choices[0].Message.Content)), a.Usage.CompletionTokens)
	assert.Equal(t, a.Usage.PromptTokens+a.Usage.CompletionTokens, a.Usage.TotalTokens)
}

func TestServer_Chat_DefaultModelWhenMissing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := decodeCompletion(t, e.chat(`{"messages":[{"role":"user","content":"x"}]}`, nil))
	assert.Equal(t, "fake-default", c.Model)
}

func TestServer_Chat_InvalidRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		hdr  map[string]string
	}{
		{"bad json", `{`, nil},
		{"unknown scenario", userBody("m", "x", ""), map[string]string{"X-Fake-Scenario": "nope"}},
		{"bad header int", userBody("m", "x", ""), map[string]string{"X-Fake-Chunk-Bytes": "abc"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			r := e.chat(tc.body, tc.hdr)
			assert.Equal(t, http.StatusBadRequest, r.status)
			assert.Contains(t, r.body, `"error"`)
		})
	}
}

func TestServer_Chat_Echo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{"string", `{"model":"m","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"a"},` +
			`{"role":"user","content":"  Hé\n\"llo\" <&>   "}]}`, "  Hé\n\"llo\" <&>   "},
		{"parts", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"ab"},` +
			`{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"cd"}]}]}`, "abcd"},
		{"no user", `{"model":"m","messages":[{"role":"system","content":"s"}]}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			c := decodeCompletion(t, e.chat(tc.body, map[string]string{"X-Fake-Scenario": "echo"}))
			require.NotNil(t, c.Choices[0].Message.Content)
			assert.Equal(t, tc.want, *c.Choices[0].Message.Content)
		})
	}
}

func TestServer_Chat_ToolCall(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tools := `,"tools":[{"type":"function","function":{"name":"lookup","parameters":{}}},` +
		`{"type":"function","function":{"name":"other"}}]`
	text := "say \"hi\" <b>\nnow"
	c := decodeCompletion(t, e.chat(userBody("m", text, tools), map[string]string{"X-Fake-Scenario": "tool-call"}))
	ch := c.Choices[0]
	assert.Equal(t, "tool_calls", ch.FinishReason)
	assert.Nil(t, ch.Message.Content)
	require.Len(t, ch.Message.ToolCalls, 1)
	tc := ch.Message.ToolCalls[0]
	assert.Equal(t, "lookup", tc.Function.Name)
	assert.Equal(t, "function", tc.Type)
	assert.NotEmpty(t, tc.ID)
	assert.JSONEq(t, `{"input":"say \"hi\" <b>\nnow"}`, tc.Function.Arguments)
	assert.True(t, strings.HasPrefix(tc.Function.Arguments, `{"input": "`), tc.Function.Arguments)
	assert.Positive(t, c.Usage.CompletionTokens)
}

func TestServer_Chat_ToolCallWithoutTools(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := decodeCompletion(t, e.chat(userBody("m", "x", ""), map[string]string{"X-Fake-Scenario": "tool-call"}))
	assert.Equal(t, "stop", c.Choices[0].FinishReason)
	assert.Empty(t, c.Choices[0].Message.ToolCalls)
}

func TestServer_Chat_ToolCallStreaming(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tools := `,"stream":true,"tools":[{"type":"function","function":{"name":"lookup"}}]`
	r := e.chat(userBody("m", "a fairly long user text", tools),
		map[string]string{"X-Fake-Scenario": "tool-call", "X-Fake-Chunk-Bytes": "5"})
	chunks := decodeStream(t, r)
	var args strings.Builder
	n := 0
	var name, finish string
	for _, c := range chunks {
		ch := c.Choices[0]
		if len(ch.Delta.ToolCalls) > 0 {
			n++
			if fn := ch.Delta.ToolCalls[0].Function.Name; fn != "" {
				name = fn
			}
			args.WriteString(ch.Delta.ToolCalls[0].Function.Arguments)
		}
		if ch.FinishReason != nil {
			finish = *ch.FinishReason
		}
	}
	assert.Equal(t, "lookup", name)
	assert.Equal(t, "tool_calls", finish)
	assert.JSONEq(t, `{"input":"a fairly long user text"}`, args.String())
	assert.Greater(t, n, 2, "arguments split into deltas")
}

func TestServer_Chat_SlowWaitsForLatency(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	done := make(chan reply, 1)
	go func() {
		done <- e.chat(userBody("m", "x", ""), map[string]string{"X-Fake-Scenario": "slow", "X-Fake-Latency-Ms": "500"})
	}()
	testutil.Eventually(t, func() bool { return e.clk.Waiters() == 1 }, 5*time.Second)
	select {
	case <-done:
		t.Fatal("responded before latency elapsed")
	default:
	}
	e.clk.Advance(499 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("responded before latency elapsed")
	case <-time.After(50 * time.Millisecond):
	}
	e.clk.Advance(time.Millisecond)
	r := <-done
	assert.Equal(t, http.StatusOK, r.status)
}

func TestServer_Chat_TimeoutUntilDisconnect(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx, cancel := withCancel(t)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.ts.URL+"/v1/chat/completions",
		strings.NewReader(userBody("m", "x", "")))
	require.NoError(t, err)
	req.Header.Set("X-Fake-Scenario", "timeout")
	errc := make(chan error, 1)
	go func() {
		resp, err := e.client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		errc <- err
	}()
	testutil.Eventually(t, func() bool { return e.clk.Waiters() == 1 }, 5*time.Second)
	select {
	case <-errc:
		t.Fatal("timeout scenario answered")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	require.Error(t, <-errc)
}

func TestServer_Chat_TimeoutMaxTenMinutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	errc := make(chan error, 1)
	go func() {
		_, err := e.try(http.MethodPost, "/v1/chat/completions", userBody("m", "x", ""),
			map[string]string{"X-Fake-Scenario": "timeout"})
		errc <- err
	}()
	testutil.Eventually(t, func() bool { return e.clk.Waiters() == 1 }, 5*time.Second)
	e.clk.Advance(10 * time.Minute)
	require.Error(t, <-errc, "connection is aborted after the maximum")
}

func TestServer_Chat_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scenario string
		status   int
		typ      string
		retry    string
	}{
		{"429", http.StatusTooManyRequests, "rate_limit_error", "1"},
		{"500", http.StatusInternalServerError, "server_error", ""},
	}
	for _, tc := range tests {
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			r := e.chat(userBody("m", "x", ""), map[string]string{"X-Fake-Scenario": tc.scenario})
			assert.Equal(t, tc.status, r.status)
			assert.Equal(t, tc.retry, r.header.Get("Retry-After"))
			assert.Contains(t, r.body, `"type":"`+tc.typ+`"`)
			assert.Contains(t, r.body, `"message"`)
		})
	}
}

func TestServer_Chat_CutStream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cutAfter string
		want     int
	}{
		{"after 1", "1", 1},
		{"after 3", "3", 3},
		{"after 0", "0", 0},
		{"default is 1", "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			hdr := map[string]string{"X-Fake-Scenario": "cut-stream", "X-Fake-Chunk-Bytes": "4"}
			if tc.cutAfter != "" {
				hdr["X-Fake-Cut-After"] = tc.cutAfter
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.ts.URL+"/v1/chat/completions",
				strings.NewReader(userBody("m", "x", `,"stream":true`)))
			require.NoError(t, err)
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			resp, err := e.client.Do(req)
			if tc.want == 0 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			var b strings.Builder
			buf := make([]byte, 4096)
			var rerr error
			for rerr == nil {
				var n int
				n, rerr = resp.Body.Read(buf)
				b.Write(buf[:n])
			}
			require.Error(t, rerr)
			require.NotErrorIs(t, rerr, errEOF)
			lines := sseLines(b.String())
			assert.Len(t, lines, tc.want)
			assert.NotContains(t, b.String(), "[DONE]")
		})
	}
}

func TestServer_Chat_CutStreamNonStreaming(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, err := e.try(http.MethodPost, "/v1/chat/completions", userBody("m", "x", ""),
		map[string]string{"X-Fake-Scenario": "cut-stream"})
	require.Error(t, err)
}

func TestServer_Chat_InvalidJSON(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r := e.chat(userBody("m", "x", ""), map[string]string{"X-Fake-Scenario": "invalid-json"})
	assert.Equal(t, http.StatusOK, r.status)
	require.Error(t, jsonValid(r.body))
	assert.True(t, strings.HasPrefix(r.body, "{"))

	s := e.chat(userBody("m", "x", `,"stream":true`), map[string]string{"X-Fake-Scenario": "invalid-json"})
	assert.Equal(t, http.StatusOK, s.status)
	lines := sseLines(s.body)
	require.Len(t, lines, 1)
	require.Error(t, jsonValid(lines[0]))
	assert.NotContains(t, s.body, "[DONE]")
}

func TestServer_Chat_StreamingShape(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r := e.chat(userBody("m-s", "one two three", `,"stream":true,"stream_options":{"include_usage":true}`),
		map[string]string{"X-Fake-Answer": "alpha beta gamma delta"})
	assert.Equal(t, "text/event-stream", r.header.Get("Content-Type"))
	assert.True(t, strings.HasSuffix(r.body, "data: [DONE]\n\n"))
	chunks := decodeStream(t, r)
	require.GreaterOrEqual(t, len(chunks), 3)
	for _, c := range chunks {
		assert.Equal(t, "chat.completion.chunk", c.Object)
		assert.Equal(t, "m-s", c.Model)
	}
	assert.Equal(t, "assistant", chunks[0].Choices[0].Delta.Role)
	assert.Equal(t, "alpha beta gamma delta", streamText(chunks))
	last := chunks[len(chunks)-1]
	assert.Empty(t, last.Choices)
	require.NotNil(t, last.Usage)
	assert.Equal(t, 3, last.Usage.PromptTokens)
	assert.Equal(t, 4, last.Usage.CompletionTokens)
	assert.Equal(t, 7, last.Usage.TotalTokens)
	fin := chunks[len(chunks)-2].Choices[0].FinishReason
	require.NotNil(t, fin)
	assert.Equal(t, "stop", *fin)
}

func TestServer_Chat_StreamingWithoutUsage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	chunks := decodeStream(t, e.chat(userBody("m", "x", `,"stream":true`), nil))
	for _, c := range chunks {
		assert.Nil(t, c.Usage)
		assert.Len(t, c.Choices, 1)
	}
}

func TestServer_Chat_ChunkBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		answer string
		hdr    map[string]string
		want   []string
	}{
		{
			"default 16", strings.Repeat("a", 40), nil,
			[]string{strings.Repeat("a", 16), strings.Repeat("a", 16), strings.Repeat("a", 8)},
		},
		{"custom", "abcdefg", map[string]string{"X-Fake-Chunk-Bytes": "3"}, []string{"abc", "def", "g"}},
		{"rune boundaries respected", "aé€😀z", map[string]string{"X-Fake-Chunk-Bytes": "2"}, []string{"aé", "€", "😀", "z"}},
		{
			"split runes", "é€",
			map[string]string{"X-Fake-Chunk-Bytes": "2", "X-Fake-Chunk-Split-Runes": "true"},
			[]string{"\xc3\xa9", "\xe2\x82", "\xac"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			hdr := map[string]string{"X-Fake-Answer": tc.answer}
			for k, v := range tc.hdr {
				hdr[k] = v
			}
			r := e.chat(userBody("m", "x", `,"stream":true`), hdr)
			var got []string
			for _, l := range sseLines(r.body) {
				if l == "[DONE]" || !strings.Contains(l, `"content":"`) {
					continue
				}
				_, rest, _ := strings.Cut(l, `"content":"`)
				piece, _, _ := strings.Cut(rest, `"}`)
				got = append(got, piece)
			}
			assert.Equal(t, tc.want, got)
			joined := strings.Join(got, "")
			assert.Equal(t, tc.answer, joined)
			if tc.name != "split runes" {
				for _, g := range got {
					assert.True(t, utf8.ValidString(g))
				}
			}
		})
	}
}

func TestServer_Chat_ChunkDelay(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	done := make(chan reply, 1)
	go func() {
		done <- e.chat(userBody("m", "x", `,"stream":true`), map[string]string{
			"X-Fake-Answer": "abcdef", "X-Fake-Chunk-Bytes": "3", "X-Fake-Chunk-Delay-Ms": "100",
		})
	}()
	for range 2 {
		testutil.Eventually(t, func() bool { return e.clk.Waiters() == 1 }, 5*time.Second)
		e.clk.Advance(100 * time.Millisecond)
	}
	r := <-done
	assert.Equal(t, "abcdef", streamText(decodeStream(t, r)))
}

func TestServer_Chat_AnswerTemplate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	hdr := map[string]string{"X-Fake-Answer": "req {n} for {user}"}
	for i, want := range []string{"req 1 for hi {n}", "req 2 for hi {n}"} {
		c := decodeCompletion(t, e.chat(userBody("m", "hi {n}", ""), hdr))
		assert.Equal(t, want, *c.Choices[0].Message.Content, "request %d", i)
	}
	other := decodeCompletion(t, e.chat(userBody("other", "yo", ""), hdr))
	assert.Equal(t, "req 1 for yo", *other.Choices[0].Message.Content, "counter is per model")

	empty := decodeCompletion(t, e.chat(userBody("m", "x", ""), map[string]string{"X-Fake-Answer": ""}))
	assert.Empty(t, *empty.Choices[0].Message.Content)
}

func TestServer_Chat_CounterResetByDeleteScript(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	hdr := map[string]string{"X-Fake-Answer": "{n}"}
	for _, want := range []string{"1", "2", "3"} {
		assert.Equal(t, want, *decodeCompletion(t, e.chat(userBody("m", "x", ""), hdr)).Choices[0].Message.Content)
	}
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/_fake/script", "", nil).status)
	assert.Equal(t, "1", *decodeCompletion(t, e.chat(userBody("m", "x", ""), hdr)).Choices[0].Message.Content)
}

func TestServer_Chat_PromptTokensCountAllMessages(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"model":"m","messages":[{"role":"system","content":"a b"},` +
		`{"role":"user","content":[{"type":"text","text":"c d"},{"type":"text","text":" e"}]},` +
		`{"role":"assistant","content":null}]}`
	c := decodeCompletion(t, e.chat(body, nil))
	assert.Equal(t, 5, c.Usage.PromptTokens)
}
