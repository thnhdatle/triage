// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

type stubProvider struct {
	provider.Provider
	resp   chat.Response
	chunks []chat.Chunk
}

func (s stubProvider) Complete(context.Context, chat.Request) (chat.Response, error) {
	return s.resp, nil
}

func (s stubProvider) Stream(context.Context, chat.Request) (provider.Stream, error) {
	return &stubStream{chunks: s.chunks}, nil
}

type stubStream struct{ chunks []chat.Chunk }

func (s *stubStream) Recv() (chat.Chunk, error) {
	if len(s.chunks) == 0 {
		return chat.Chunk{}, io.EOF
	}
	c := s.chunks[0]
	s.chunks = s.chunks[1:]
	return c, nil
}

func (*stubStream) Close() error { return nil }

func tracedFixture(t *testing.T, capture bool) (*tracedProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tel, err := telemetry.Setup(context.Background(), telemetry.Options{SpanExporter: exp, CaptureContent: capture})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	next := stubProvider{
		resp: chat.Response{Model: "m", Choices: []chat.Choice{{
			Message: chat.TextMessage(chat.RoleAssistant, "hello <EMAIL_1>"), FinishReason: chat.FinishStop,
		}}},
		chunks: []chat.Chunk{
			{Choices: []chat.ChunkChoice{{Delta: chat.Delta{Content: "he"}}}},
			{Choices: []chat.ChunkChoice{{Delta: chat.Delta{Content: "llo"}, FinishReason: chat.FinishStop}}},
		},
	}
	return &tracedProvider{next: next, name: "p", tel: tel, clk: clock.Real(), targets: map[string]string{}}, exp
}

func spanAttr(t *testing.T, exp *tracetest.InMemoryExporter, key string) (string, bool) {
	t.Helper()
	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	for _, a := range spans[0].Attributes {
		if string(a.Key) == key {
			return a.Value.AsString(), true
		}
	}
	return "", false
}

var maskedReq = chat.Request{Model: "m", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "mail <EMAIL_1>")}}

func TestTracedProvider_CaptureContentComplete(t *testing.T) {
	t.Parallel()
	tp, exp := tracedFixture(t, true)
	_, err := tp.Complete(context.Background(), maskedReq)
	require.NoError(t, err)
	in, ok := spanAttr(t, exp, attrInputMessages)
	require.True(t, ok)
	assert.JSONEq(t, `[{"role":"user","parts":[{"type":"text","content":"mail <EMAIL_1>"}]}]`, in)
	out, ok := spanAttr(t, exp, attrOutputMessages)
	require.True(t, ok)
	assert.JSONEq(t,
		`[{"role":"assistant","parts":[{"type":"text","content":"hello <EMAIL_1>"}],"finish_reason":"stop"}]`, out)
}

func TestTracedProvider_CaptureContentStream(t *testing.T) {
	t.Parallel()
	tp, exp := tracedFixture(t, true)
	s, err := tp.Stream(context.Background(), maskedReq)
	require.NoError(t, err)
	for {
		if _, err := s.Recv(); err != nil {
			break
		}
	}
	require.NoError(t, s.Close())
	_, ok := spanAttr(t, exp, attrInputMessages)
	assert.True(t, ok)
	out, ok := spanAttr(t, exp, attrOutputMessages)
	require.True(t, ok)
	assert.JSONEq(t, `[{"role":"assistant","parts":[{"type":"text","content":"hello"}],"finish_reason":"stop"}]`, out)
}

func TestTracedProvider_ContentOffByDefault(t *testing.T) {
	t.Parallel()
	tp, exp := tracedFixture(t, false)
	_, err := tp.Complete(context.Background(), maskedReq)
	require.NoError(t, err)
	_, in := spanAttr(t, exp, attrInputMessages)
	_, out := spanAttr(t, exp, attrOutputMessages)
	assert.False(t, in || out)
}
