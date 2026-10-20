// SPDX-License-Identifier: Apache-2.0

package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/testutil/fakedetector"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

// assertStreamIdentity checks that every relayed chunk carries the id and
// model of the first relayed chunk and that the role appears once.
func assertStreamIdentity(t *testing.T, s *sink, wantModel string) {
	t.Helper()
	roles := 0
	for _, c := range s.chunks {
		assert.Equal(t, wantModel, c.Model)
		assert.Equal(t, "id-"+wantModel, c.ID)
		for _, ch := range c.Choices {
			if ch.Delta.Role != "" {
				roles++
			}
		}
	}
	assert.Equal(t, 1, roles, "role delta is relayed once")
}

func TestDispatcher_Stream_RetriesBeforeFirstContentByte(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		first        fakeprovider.Outcome
		wantStatuses []string
	}{
		{name: "429 on open", first: fakeprovider.Fail(errRate), wantStatuses: []string{"a:rate_limited", "b:ok"}},
		{name: "500 on open", first: fakeprovider.Fail(errServer), wantStatuses: []string{"a:unavailable", "b:ok"}},
		{
			name: "cut before any chunk", first: stream(nil, errUnavailable),
			wantStatuses: []string{"a:unavailable", "b:ok"},
		},
		{
			name: "cut after the role chunk", first: stream(cutChunks("a-up"), errUnavailable),
			wantStatuses: []string{"a:unavailable", "b:ok"},
		},
		{name: "timeout error", first: fakeprovider.Fail(errTimeout), wantStatuses: []string{"a:timeout", "b:ok"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(tc.first)
			b := scripted(stream(textChunks("b-up", "Hel", "lo"), nil))
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{})
			s := &sink{}
			res, err := d.Stream(context.Background(), plan(userRequest("x"), local("a"), local("b")), s)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatuses, statuses(res))
			require.Len(t, s.starts, 1)
			assert.Equal(t, "b", s.starts[0].Name)
			assert.Equal(t, "Hello", s.text())
			assertStreamIdentity(t, s, "b-up")
			assert.Empty(t, s.errs)
			assert.False(t, res.Attempts[1].Continuation)
			assert.Equal(t, "b-up", res.Model)
			assert.Equal(t, "b", res.Target.Name)
			assert.True(t, b.Requests()[0].Stream)
		})
	}
}

func TestDispatcher_Stream_NoRetryOnClientErrors(t *testing.T) {
	t.Parallel()
	for _, e := range []error{errBad, errCtxLen} {
		a := scripted(fakeprovider.Fail(e))
		b := fakeprovider.New()
		d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{})
		s := &sink{}
		_, err := d.Stream(context.Background(), plan(userRequest("x"), local("a"), local("b")), s)
		require.ErrorIs(t, err, e)
		var de *dispatch.Error
		require.ErrorAs(t, err, &de)
		assert.False(t, de.Started)
		assert.Empty(t, s.starts, "nothing relayed: the caller writes an HTTP error")
		assert.Empty(t, s.errs)
		assert.Empty(t, b.Requests())
	}
}

func TestDispatcher_Stream_CutAfterKChunks(t *testing.T) {
	t.Parallel()
	parts := []string{"one ", "two ", "three "}
	tests := []struct {
		name    string
		k       int
		failure dispatch.StreamFailure
	}{
		{name: "continue after 1", k: 1, failure: dispatch.StreamFailureContinue},
		{name: "continue after 3", k: 3, failure: dispatch.StreamFailureContinue},
		{name: "continue is the default", k: 2},
		{name: "error after 1", k: 1, failure: dispatch.StreamFailureError},
		{name: "error after 3", k: 3, failure: dispatch.StreamFailureError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(stream(cutChunks("a-up", parts[:tc.k]...), errUnavailable))
			b := scripted(stream(textChunks("b-up", "rest"), nil))
			h := &health{}
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b},
				dispatch.Options{StreamFailure: tc.failure, Health: h})
			s := &sink{}
			req := userRequest("count")
			res, err := d.Stream(context.Background(), plan(req, local("a"), cloud("b")), s)
			produced := strings.Join(parts[:tc.k], "")
			require.Len(t, s.starts, 1)
			assert.Equal(t, "a", s.starts[0].Name)
			if tc.failure == dispatch.StreamFailureError {
				var de *dispatch.Error
				require.ErrorAs(t, err, &de)
				require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
				assert.True(t, de.Started)
				require.Len(t, s.errs, 1)
				assert.Equal(t, produced, s.text())
				assert.Empty(t, b.Requests())
				assert.Equal(t, []string{"a:cut"}, statuses(res))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, produced+"rest", s.text())
			assertStreamIdentity(t, s, "a-up")
			assert.Equal(t, []string{"a:cut", "b:ok"}, statuses(res))
			assert.True(t, res.Attempts[1].Continuation)
			assert.False(t, res.Attempts[1].Masked)
			assert.Equal(t, "a-up", res.Model, "chunks carry the first relayed model")
			assert.Equal(t, "b", res.Target.Name)
			assert.Equal(t, []string{"a:fail", "b:ok"}, h.all())
			cont := b.Requests()[0]
			require.Len(t, cont.Messages, len(req.Messages)+2)
			assert.Equal(t, req.Messages, cont.Messages[:len(req.Messages)])
			assert.Equal(t, chat.TextMessage(chat.RoleAssistant, produced), cont.Messages[len(req.Messages)])
			instr := chat.TextMessage(chat.RoleUser, dispatch.ContinuationInstruction)
			assert.Equal(t, instr, cont.Messages[len(req.Messages)+1])
			assert.Equal(t, "b-model", cont.Model)
		})
	}
}

func TestDispatcher_Stream_CutWithoutContinuation(t *testing.T) {
	t.Parallel()
	toolChunks := []chat.Chunk{{
		ID: "id-a-up", Model: "a-up",
		Choices: []chat.ChunkChoice{{Delta: chat.Delta{
			Role:      chat.RoleAssistant,
			ToolCalls: []chat.ToolCallDelta{{Index: 0, ID: "call_1", Type: "function", Name: "f", Arguments: `{"a":`}},
		}}},
	}}
	multiChunks := []chat.Chunk{{
		ID: "id-a-up", Model: "a-up",
		Choices: []chat.ChunkChoice{
			{Index: 0, Delta: chat.Delta{Role: chat.RoleAssistant, Content: "x"}},
			{Index: 1, Delta: chat.Delta{Role: chat.RoleAssistant, Content: "y"}},
		},
	}}
	tests := []struct {
		name   string
		chunks []chat.Chunk
	}{
		{name: "cut inside a tool call", chunks: toolChunks},
		{name: "cut in a multi-choice stream", chunks: multiChunks},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(stream(tc.chunks, errUnavailable))
			b := fakeprovider.New()
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{})
			s := &sink{}
			res, err := d.Stream(context.Background(), plan(userRequest("x"), local("a"), local("b")), s)
			require.ErrorIs(t, err, provider.ErrUpstreamUnavailable)
			assert.Len(t, s.errs, 1)
			assert.Empty(t, b.Requests())
			assert.Equal(t, []string{"a:cut"}, statuses(res))
		})
	}
}

func TestDispatcher_Stream_ContinuationToCloudIsMasked(t *testing.T) {
	t.Parallel()
	// The local attempt echoes the finding and produces a new value, then is
	// cut; the cloud continuation must see neither value, and its output
	// (with placeholders, one split across chunks) is unmasked for the client.
	l := scripted(stream(
		cutChunks("l-up", "to "+secret, " cc "+secretOther), errUnavailable))
	c := scripted(stream(
		textChunks("c-up", " and [[EMA", "IL_1]] again [[EMAIL_2]]"), nil))
	d, _ := newDispatcher(t, map[string]provider.Provider{"l": l, "c": c}, dispatch.Options{})
	s := &sink{}
	res, err := d.Stream(context.Background(), maskedPlan(t, local("l"), cloud("c")), s)
	require.NoError(t, err)
	assert.Equal(t, []string{"l:cut", "c:ok"}, statuses(res))
	assert.Equal(t, []bool{false, true}, []bool{res.Attempts[0].Masked, res.Attempts[1].Masked})

	sent := requestText(c.Requests()[0])
	assert.NotContains(t, sent, secret)
	assert.NotContains(t, sent, secretOther)
	assert.Contains(t, sent, "to [[EMAIL_1]] cc [[EMAIL_2]]", "partial assistant text is masked")
	assert.Contains(t, requestText(l.Requests()[0]), secret)

	assert.Equal(t, "to "+secret+" cc "+secretOther+" and "+secret+" again "+secretOther, s.text())
	assert.Equal(t, decision.UnmaskOK, res.Unmask)
	assert.Empty(t, s.comments)
}

func TestDispatcher_Stream_ContinuationRescanFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		detector privacy.Detector
		chain    []model.Target
		wantErr  error
		statuses []string
	}{
		{
			name:     "ner unavailable falls back to local",
			detector: fakedetector.NewLiteral("ner", privacy.TypeNERUnavailable, "Alice"),
			chain:    []model.Target{local("l"), cloud("c"), local("l2")},
			statuses: []string{"l:cut", "c:rescan_incomplete", "l2:ok"},
		},
		{
			name:     "detector error falls back to local",
			detector: fakedetector.NewLiteral("fake", "email", "zzz").WithError(errors.New("detector down")),
			chain:    []model.Target{local("l"), cloud("c"), local("l2")},
			statuses: []string{"l:cut", "c:rescan_error", "l2:ok"},
		},
		{
			name:     "no local target left",
			detector: fakedetector.NewLiteral("ner", privacy.TypeNERUnavailable, "Alice"),
			chain:    []model.Target{cloud("l"), cloud("c")},
			statuses: []string{"l:cut", "c:rescan_incomplete"},
			wantErr:  privacy.ErrRescanIncomplete,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := scripted(stream(cutChunks("l-up", "Alice says"), errUnavailable))
			c := fakeprovider.New()
			l2 := scripted(stream(textChunks("l2-up", " hi"), nil))
			d, _ := newDispatcher(t, map[string]provider.Provider{"l": l, "c": c, "l2": l2}, dispatch.Options{})
			req := userRequest("who")
			p := plan(req, tc.chain...)
			p.Detectors = []privacy.Detector{tc.detector}
			s := &sink{}
			res, err := d.Stream(context.Background(), p, s)
			assert.Equal(t, tc.statuses, statuses(res))
			assert.Empty(t, c.Requests())
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.ErrorIs(t, err, dispatch.ErrRescan)
				assert.Len(t, s.errs, 1)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "Alice says hi", s.text())
		})
	}
}

func TestDispatcher_Stream_ContinuationWithoutFindingsCreatesMapping(t *testing.T) {
	t.Parallel()
	// Mask mode without findings in the request: the local output contains a
	// value, which the cloud continuation must not see.
	l := scripted(stream(cutChunks("l-up", "write "+secret), errUnavailable))
	c := scripted(stream(textChunks("c-up", " now"), nil))
	d, _ := newDispatcher(t, map[string]provider.Provider{"l": l, "c": c}, dispatch.Options{})
	p := plan(userRequest("x"), local("l"), cloud("c"))
	p.Detectors = []privacy.Detector{fakedetector.NewLiteral("fake", "email", secret)}
	s := &sink{}
	res, err := d.Stream(context.Background(), p, s)
	require.NoError(t, err)
	assert.NotContains(t, requestText(c.Requests()[0]), secret)
	assert.True(t, res.Attempts[1].Masked)
	assert.Equal(t, "write "+secret+" now", s.text())
}

func TestDispatcher_Stream_MaskedCutFlushesHeldBytes(t *testing.T) {
	t.Parallel()
	// The cloud attempt is cut while the unmasker holds a placeholder prefix;
	// the held bytes are relayed before the continuation.
	c := scripted(stream(cutChunks("c-up", "hi [[EMA"), errUnavailable))
	l := scripted(stream(textChunks("l-up", "!"), nil))
	d, _ := newDispatcher(t, map[string]provider.Provider{"l": l, "c": c}, dispatch.Options{})
	s := &sink{}
	res, err := d.Stream(context.Background(), maskedPlan(t, cloud("c"), local("l")), s)
	require.NoError(t, err)
	assert.Equal(t, "hi [[EMA!", s.text())
	cont := l.Requests()[0]
	assert.Equal(t, "hi [[EMA", cont.Messages[len(cont.Messages)-2].Text())
	assert.Equal(t, decision.UnmaskOK, res.Unmask)
}

func TestDispatcher_Stream_PartialUnmaskComment(t *testing.T) {
	t.Parallel()
	c := scripted(stream(textChunks("c-up", "to [[EMAIL_7]]"), nil))
	d, _ := newDispatcher(t, map[string]provider.Provider{"c": c}, dispatch.Options{})
	s := &sink{}
	res, err := d.Stream(context.Background(), maskedPlan(t, cloud("c")), s)
	require.NoError(t, err)
	assert.Equal(t, decision.UnmaskPartial, res.Unmask)
	assert.Equal(t, []string{dispatch.UnmaskPartialComment}, s.comments)
	assert.Equal(t, "to [[EMAIL_7]]", s.text())
}

func TestDispatcher_Stream_UsageSummedOverAttempts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		includeUsage bool
	}{
		{name: "client asked for usage", includeUsage: true},
		{name: "client did not ask", includeUsage: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u1 := chat.Usage{PromptTokens: 10, CompletionTokens: 2}
			u2 := chat.Usage{PromptTokens: 1000, CompletionTokens: 500, CachedPromptTokens: 400}
			first := cutChunks("a-up", "par")
			first[1].Usage = &u1 // usage reported mid-stream before the cut
			a := scripted(stream(first, errUnavailable))
			b := scripted(stream(
				append(textChunks("b-up", "tial"), usageChunk("b-up", u2)), nil))
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{})
			at := local("a")
			at.Pricing = model.Pricing{InputPerMTok: 1000, OutputPerMTok: 1000}
			bt := cloud("b")
			bt.Pricing = model.Pricing{InputPerMTok: 1, OutputPerMTok: 2, CachedInputPerMTok: 0.5}
			req := userRequest("x")
			req.IncludeUsage = tc.includeUsage
			s := &sink{}
			res, err := d.Stream(context.Background(), plan(req, at, bt), s)
			require.NoError(t, err)
			want := u1.Add(u2)
			assert.Equal(t, want, res.Usage)
			assert.InDelta(t, 0.012, res.Attempts[0].CostUSD, 1e-12)
			assert.InDelta(t, 0.0018, res.Attempts[1].CostUSD, 1e-12)
			assert.InDelta(t, 0.0138, res.CostUSD, 1e-12)
			assert.True(t, a.Requests()[0].IncludeUsage, "usage is always requested upstream")
			last := s.chunks[len(s.chunks)-1]
			if !tc.includeUsage {
				assert.Nil(t, last.Usage)
				for _, c := range s.chunks {
					assert.Nil(t, c.Usage)
				}
				return
			}
			require.NotNil(t, last.Usage)
			assert.Equal(t, want, *last.Usage)
			assert.Empty(t, last.Choices)
			assert.Equal(t, "a-up", last.Model)
			for _, c := range s.chunks[:len(s.chunks)-1] {
				assert.Nil(t, c.Usage, "upstream usage chunks are not relayed")
			}
		})
	}
}

func TestDispatcher_Stream_TimeoutBeforeFirstChunk(t *testing.T) {
	t.Parallel()
	slow := newBlocking()
	b := scripted(stream(textChunks("b-up", "ok"), nil))
	d, clk := newDispatcher(t, map[string]provider.Provider{"slow": slow, "b": b},
		dispatch.Options{AttemptTimeout: time.Second})
	s := &sink{}
	done := async(func() (dispatch.Result, error) {
		return d.Stream(context.Background(), plan(userRequest("x"), local("slow"), local("b")), s)
	})
	waitEntered(t, slow)
	clk.Advance(time.Second)
	o := await(t, done)
	require.NoError(t, o.err)
	assert.Equal(t, []string{"slow:timeout", "b:ok"}, statuses(o.res))
	assert.Equal(t, "b", s.starts[0].Name)
	assert.Equal(t, "ok", s.text())
}

func TestDispatcher_Stream_TimeoutBetweenChunks(t *testing.T) {
	t.Parallel()
	slow := newBlocking(cutChunks("slow-up", "a", "b")...)
	b := scripted(stream(textChunks("b-up", "c"), nil))
	slowT := local("slow")
	slowT.AttemptTimeout = 3 * time.Second
	d, clk := newDispatcher(t, map[string]provider.Provider{"slow": slow, "b": b}, dispatch.Options{})
	s := &sink{}
	done := async(func() (dispatch.Result, error) {
		return d.Stream(context.Background(), plan(userRequest("x"), slowT, local("b")), s)
	})
	waitEntered(t, slow)
	assert.Equal(t, 2, s.chunkCount())
	clk.Advance(3 * time.Second)
	o := await(t, done)
	require.NoError(t, o.err)
	assert.Equal(t, []string{"slow:cut", "b:ok"}, statuses(o.res))
	assert.Equal(t, "abc", s.text())
	assert.Equal(t, 3*time.Second, o.res.Attempts[0].Latency)
}

func TestDispatcher_Stream_ContextCanceledAfterStart(t *testing.T) {
	t.Parallel()
	slow := newBlocking(cutChunks("slow-up", "a")...)
	d, _ := newDispatcher(t, map[string]provider.Provider{"slow": slow, "b": fakeprovider.New()}, dispatch.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	s := &sink{}
	done := async(func() (dispatch.Result, error) {
		return d.Stream(ctx, plan(userRequest("x"), local("slow"), local("b")), s)
	})
	waitEntered(t, slow)
	cancel()
	o := await(t, done)
	require.ErrorIs(t, o.err, context.Canceled)
	assert.Empty(t, s.errs, "no error event for a client that went away")
	assert.Equal(t, []string{"slow:canceled"}, statuses(o.res))
}

func TestDispatcher_Stream_TotalTimeoutAfterStart(t *testing.T) {
	t.Parallel()
	slow := newBlocking(cutChunks("slow-up", "a")...)
	d, clk := newDispatcher(t, map[string]provider.Provider{"slow": slow}, dispatch.Options{TotalTimeout: 5 * time.Second})
	s := &sink{}
	done := async(func() (dispatch.Result, error) {
		return d.Stream(context.Background(), plan(userRequest("x"), local("slow")), s)
	})
	waitEntered(t, slow)
	clk.Advance(5 * time.Second)
	o := await(t, done)
	require.ErrorIs(t, o.err, dispatch.ErrTotalTimeout)
	require.Len(t, s.errs, 1)
	require.ErrorIs(t, s.errs[0], dispatch.ErrTotalTimeout)
}

func TestDispatcher_Stream_ExhaustedAfterStart(t *testing.T) {
	t.Parallel()
	a := scripted(stream(cutChunks("a-up", "x"), errUnavailable))
	b := scripted(fakeprovider.Fail(errServer))
	d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{MaxAttempts: 2})
	s := &sink{}
	res, err := d.Stream(context.Background(), plan(userRequest("x"), local("a"), local("b")), s)
	var de *dispatch.Error
	require.ErrorAs(t, err, &de)
	assert.True(t, de.Started)
	assert.Len(t, s.errs, 1)
	assert.Equal(t, []string{"a:cut", "b:unavailable"}, statuses(res))
	assert.True(t, res.Attempts[1].Continuation)
}

func TestDispatcher_Stream_EdgeCompletions(t *testing.T) {
	t.Parallel()
	finishedThenCut := textChunks("a-up", "done")
	tests := []struct {
		name     string
		outcome  fakeprovider.Outcome
		wantText string
		statuses []string
	}{
		{
			name: "finish relayed, then cut", outcome: stream(finishedThenCut, errUnavailable),
			wantText: "done", statuses: []string{"a:ok"},
		},
		{name: "empty upstream stream", outcome: stream(nil, nil), statuses: []string{"a:ok"}},
		{name: "role only stream", outcome: stream(cutChunks("a-up"), nil), statuses: []string{"a:ok"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(tc.outcome)
			b := fakeprovider.New()
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{})
			s := &sink{}
			res, err := d.Stream(context.Background(), plan(userRequest("x"), local("a"), local("b")), s)
			require.NoError(t, err)
			assert.Equal(t, tc.statuses, statuses(res))
			require.Len(t, s.starts, 1)
			assert.Equal(t, "a", s.starts[0].Name)
			assert.Equal(t, tc.wantText, s.text())
			assert.Empty(t, b.Requests())
			assert.Equal(t, decision.UnmaskNone, res.Unmask)
		})
	}
}

func TestDispatcher_Stream_SinkErrors(t *testing.T) {
	t.Parallel()
	errSink := errors.New("client gone")
	tests := []struct {
		name    string
		sink    *sink
		outcome fakeprovider.Outcome
		usage   bool
	}{
		{name: "start", sink: &sink{startErr: errSink}, outcome: stream(textChunks("a-up", "x"), nil)},
		{
			name: "chunk", sink: &sink{chunkErr: errSink, failChunkAt: 1},
			outcome: stream(textChunks("a-up", "x"), nil),
		},
		{name: "start of empty stream", sink: &sink{startErr: errSink}, outcome: stream(nil, nil)},
		{
			name: "role of empty stream", sink: &sink{chunkErr: errSink, failChunkAt: 1},
			outcome: stream(cutChunks("a-up"), nil),
		},
		{
			name: "usage chunk", sink: &sink{chunkErr: errSink, failChunkAt: 3}, usage: true,
			outcome: stream(textChunks("a-up", "x"), nil),
		},
		{
			name: "flush on cut", sink: &sink{chunkErr: errSink, failChunkAt: 2},
			outcome: stream(cutChunks("c-up", "hi[[EMA"), errUnavailable),
		},
		{
			name: "flush on end", sink: &sink{chunkErr: errSink, failChunkAt: 2},
			outcome: stream(cutChunks("c-up", "hi[[EMA"), nil),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := scripted(tc.outcome)
			b := fakeprovider.New()
			d, _ := newDispatcher(t, map[string]provider.Provider{"a": a, "b": b}, dispatch.Options{})
			p := maskedPlan(t, cloud("a"), local("b"))
			p.Request.IncludeUsage = tc.usage
			_, err := d.Stream(context.Background(), p, tc.sink)
			require.ErrorIs(t, err, errSink)
			assert.Empty(t, tc.sink.errs)
			assert.Empty(t, b.Requests())
		})
	}
}

func TestDispatcher_Stream_ContinuationCyclesToSameTargetAfterBackoff(t *testing.T) {
	t.Parallel()
	a := scripted(
		stream(cutChunks("a-up", "first"), errUnavailable),
		stream(textChunks("a-up2", " second"), nil),
	)
	d, clk := newDispatcher(t, map[string]provider.Provider{"a": a}, dispatch.Options{})
	s := &sink{}
	done := async(func() (dispatch.Result, error) {
		return d.Stream(context.Background(), plan(userRequest("x"), local("a")), s)
	})
	// total timer, two chunk timers of attempt 1 (role, content) plus the
	// one that saw the cut, and the backoff wait
	waitWaiters(t, clk, 5)
	clk.Advance(dispatch.DefaultBackoffBase)
	o := await(t, done)
	require.NoError(t, o.err)
	assert.Equal(t, []string{"a:cut", "a:ok"}, statuses(o.res))
	assert.Equal(t, "first second", s.text())
	assertStreamIdentity(t, s, "a-up")
	assert.Len(t, s.starts, 1)
}
