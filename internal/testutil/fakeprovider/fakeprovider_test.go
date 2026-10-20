// SPDX-License-Identifier: Apache-2.0

package fakeprovider_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

var _ provider.Provider = (*fakeprovider.Provider)(nil)

func userReq(text string) chat.Request {
	return chat.Request{Model: "m", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, text)}}
}

func contentChunk(s string) chat.Chunk {
	return chat.Chunk{Choices: []chat.ChunkChoice{{Delta: chat.Delta{Content: s}}}}
}

func drain(t *testing.T, s provider.Stream) ([]chat.Chunk, error) {
	t.Helper()
	var out []chat.Chunk
	for {
		c, err := s.Recv()
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
}

func TestProvider_Complete_DefaultEchoesLastUserText(t *testing.T) {
	t.Parallel()
	p := fakeprovider.New()
	req := chat.Request{Model: "m", Messages: []chat.Message{
		chat.TextMessage(chat.RoleUser, "first"),
		chat.TextMessage(chat.RoleAssistant, "a"),
		chat.TextMessage(chat.RoleUser, "hello"),
	}}
	resp, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "m", resp.Model)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "hello", resp.Choices[0].Message.Text())
	assert.Equal(t, chat.FinishStop, resp.Choices[0].FinishReason)
	assert.Positive(t, resp.Usage.PromptTokens)
}

func TestProvider_Complete_ScriptedQueue(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	scripted := chat.Response{ID: "scripted"}
	p := fakeprovider.New(fakeprovider.WithOutcomes(fakeprovider.Fail(boom), fakeprovider.Respond(scripted)))
	p.Enqueue(fakeprovider.Outcome{})

	_, err := p.Complete(context.Background(), userReq("1"))
	require.ErrorIs(t, err, boom)
	resp, err := p.Complete(context.Background(), userReq("2"))
	require.NoError(t, err)
	assert.Equal(t, "scripted", resp.ID)
	resp, err = p.Complete(context.Background(), userReq("3"))
	require.NoError(t, err, "empty outcome falls back to echo")
	assert.Equal(t, "3", resp.Choices[0].Message.Text())
	resp, err = p.Complete(context.Background(), userReq("4"))
	require.NoError(t, err, "empty queue falls back to echo")
	assert.Equal(t, "4", resp.Choices[0].Message.Text())
}

func TestProvider_Complete_StreamScriptAggregated(t *testing.T) {
	t.Parallel()
	u := chat.Usage{PromptTokens: 3, CompletionTokens: 4}
	tests := []struct {
		name    string
		script  fakeprovider.Outcome
		wantErr error
		wantTxt string
	}{
		{
			name: "chunks joined",
			script: fakeprovider.StreamChunks([]chat.Chunk{
				{ID: "s1", Choices: []chat.ChunkChoice{{Delta: chat.Delta{Content: "ab"}}}},
				contentChunk("cd"),
				{Choices: []chat.ChunkChoice{{FinishReason: chat.FinishLength}}},
				{Usage: &u},
			}, nil),
			wantTxt: "abcd",
		},
		{name: "script error", script: fakeprovider.StreamChunks(nil, io.ErrUnexpectedEOF), wantErr: io.ErrUnexpectedEOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := fakeprovider.New(fakeprovider.WithOutcomes(tc.script))
			resp, err := p.Complete(context.Background(), userReq("x"))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTxt, resp.Choices[0].Message.Text())
			assert.Equal(t, "s1", resp.ID)
			assert.Equal(t, chat.FinishLength, resp.Choices[0].FinishReason)
			assert.Equal(t, u, resp.Usage)
		})
	}
}

func TestProvider_Complete_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := fakeprovider.New()
	_, err := p.Complete(ctx, userReq("x"))
	require.ErrorIs(t, err, context.Canceled)
	_, err = p.Stream(ctx, userReq("x"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, p.Requests(), 2, "requests are recorded even when cancelled")
}

func TestProvider_Requests_DeepCopies(t *testing.T) {
	t.Parallel()
	p := fakeprovider.New()
	req := userReq("orig")
	req.Headers = map[string]string{"X-Triage-Route": "auto"}
	_, err := p.Complete(context.Background(), req)
	require.NoError(t, err)

	req.Messages[0].Content[0].Text = "mutated"
	req.Headers["X-Triage-Route"] = "mutated"
	got := p.Requests()
	require.Len(t, got, 1)
	assert.Equal(t, "orig", got[0].LastUserText())
	assert.Equal(t, "auto", got[0].Headers["X-Triage-Route"])

	got[0].Messages[0].Content[0].Text = "again"
	assert.Equal(t, "orig", p.Requests()[0].LastUserText())
}

func TestProvider_Stream_DefaultEchoesWithUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		includeUsage bool
		wantChunks   int
	}{
		{name: "without usage", includeUsage: false, wantChunks: 2},
		{name: "with usage", includeUsage: true, wantChunks: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := fakeprovider.New()
			req := userReq("hi")
			req.IncludeUsage = tc.includeUsage
			s, err := p.Stream(context.Background(), req)
			require.NoError(t, err)
			defer func() { require.NoError(t, s.Close()) }()
			chunks, err := drain(t, s)
			require.ErrorIs(t, err, io.EOF)
			require.Len(t, chunks, tc.wantChunks)
			assert.Equal(t, "hi", chunks[0].Choices[0].Delta.Content)
			assert.Equal(t, chat.FinishStop, chunks[1].Choices[0].FinishReason)
			assert.Equal(t, tc.includeUsage, chunks[len(chunks)-1].Usage != nil)
		})
	}
}

func TestProvider_Stream_ScriptedResponse(t *testing.T) {
	t.Parallel()
	resp := chat.Response{ID: "r", Choices: []chat.Choice{{
		Message: chat.TextMessage(chat.RoleAssistant, "scripted"), FinishReason: chat.FinishToolCalls,
	}}}
	p := fakeprovider.New(fakeprovider.WithOutcomes(fakeprovider.Respond(resp), fakeprovider.Respond(chat.Response{})))
	s, err := p.Stream(context.Background(), userReq("x"))
	require.NoError(t, err)
	chunks, err := drain(t, s)
	require.ErrorIs(t, err, io.EOF)
	assert.Equal(t, "scripted", chunks[0].Choices[0].Delta.Content)
	assert.Equal(t, chat.FinishToolCalls, chunks[1].Choices[0].FinishReason)

	s, err = p.Stream(context.Background(), userReq("x"))
	require.NoError(t, err)
	chunks, err = drain(t, s)
	require.ErrorIs(t, err, io.EOF)
	assert.Equal(t, chat.FinishStop, chunks[1].Choices[0].FinishReason, "default finish reason")
}

func TestProvider_Stream_ScriptedErrorAtStart(t *testing.T) {
	t.Parallel()
	boom := &provider.Error{Kind: provider.ErrUpstreamUnavailable}
	p := fakeprovider.New(fakeprovider.WithOutcomes(fakeprovider.Fail(boom)))
	s, err := p.Stream(context.Background(), userReq("x"))
	require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
	assert.Nil(t, s)
}

func TestProvider_Stream_ErrorAfterKChunks(t *testing.T) {
	t.Parallel()
	cut := &provider.Error{Kind: provider.ErrUpstreamUnavailable, Message: "cut"}
	tests := []struct {
		name    string
		chunks  []chat.Chunk
		err     error
		wantErr error
	}{
		{
			name:    "error after two chunks",
			chunks:  []chat.Chunk{contentChunk("a"), contentChunk("b")},
			err:     cut,
			wantErr: provider.ErrUpstreamUnavailable,
		},
		{name: "error immediately", chunks: nil, err: cut, wantErr: provider.ErrUpstreamUnavailable},
		{name: "clean eof", chunks: []chat.Chunk{contentChunk("a")}, err: nil, wantErr: io.EOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := fakeprovider.New(fakeprovider.WithOutcomes(fakeprovider.StreamChunks(tc.chunks, tc.err)))
			s, err := p.Stream(context.Background(), userReq("x"))
			require.NoError(t, err)
			got, err := drain(t, s)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Len(t, got, len(tc.chunks))
			_, err = s.Recv()
			require.ErrorIs(t, err, tc.wantErr, "terminal error is sticky")
		})
	}
}

func TestStream_Recv_ContextCancelledMidStream(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	script := fakeprovider.StreamChunks([]chat.Chunk{contentChunk("a"), contentChunk("b")}, nil)
	p := fakeprovider.New(fakeprovider.WithOutcomes(script))
	s, err := p.Stream(ctx, userReq("x"))
	require.NoError(t, err)
	_, err = s.Recv()
	require.NoError(t, err)
	cancel()
	_, err = s.Recv()
	require.ErrorIs(t, err, context.Canceled)
}

func TestStream_Close_Idempotent(t *testing.T) {
	t.Parallel()
	p := fakeprovider.New()
	s, err := p.Stream(context.Background(), userReq("x"))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	_, err = s.Recv()
	require.ErrorIs(t, err, fakeprovider.ErrStreamClosed)
}

func TestProvider_Embed_DeterministicAndRecorded(t *testing.T) {
	t.Parallel()
	p := fakeprovider.New()
	req := provider.EmbedRequest{Model: "e", Input: []string{"abcd", "hello"}, Dimensions: 5, User: "u"}
	a, err := p.Embed(context.Background(), req)
	require.NoError(t, err)
	b, err := p.Embed(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.Equal(t, "e", a.Model)
	require.Len(t, a.Vectors, 2)
	assert.Len(t, a.Vectors[0], 5)
	assert.NotEqual(t, a.Vectors[0], a.Vectors[1])
	assert.Equal(t, 1+2, a.Usage.PromptTokens)

	def, err := p.Embed(context.Background(), provider.EmbedRequest{Model: "e", Input: []string{"x"}})
	require.NoError(t, err)
	assert.Len(t, def.Vectors[0], 8)

	rec := p.EmbedRequests()
	require.Len(t, rec, 3)
	assert.Equal(t, req, rec[0])
}

func TestProvider_Embed_ErrorAndCancel(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	_, err := fakeprovider.New(fakeprovider.WithEmbedError(boom)).Embed(context.Background(), provider.EmbedRequest{})
	require.ErrorIs(t, err, boom)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fakeprovider.New().Embed(ctx, provider.EmbedRequest{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestProvider_Models_Table(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		p       *fakeprovider.Provider
		ctx     context.Context //nolint:containedctx // table field for a test context
		want    []string
		wantErr error
	}{
		{
			name: "configured",
			p:    fakeprovider.New(fakeprovider.WithModels("a", "b")),
			ctx:  context.Background(),
			want: []string{"a", "b"},
		},
		{name: "none", p: fakeprovider.New(), ctx: context.Background(), want: []string{}},
		{name: "error", p: fakeprovider.New(fakeprovider.WithModelsError(boom)), ctx: context.Background(), wantErr: boom},
		{name: "cancelled", p: fakeprovider.New(), ctx: cancelled, wantErr: context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.p.Models(tc.ctx)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestProvider_Complete_Concurrent(t *testing.T) {
	t.Parallel()
	p := fakeprovider.New()
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Complete(context.Background(), userReq("x"))
			assert.NoError(t, err)
			_ = p.Requests()
		}()
	}
	wg.Wait()
	assert.Len(t, p.Requests(), 40)
}
