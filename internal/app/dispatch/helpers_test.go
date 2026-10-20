// SPDX-License-Identifier: Apache-2.0

package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil"
	"github.com/thnhdatle/triage/internal/testutil/fakedetector"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// epoch is the start of every fake clock.
var epoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

const (
	secret      = "bob@example.com"
	secretOther = "carol@example.com"
	waitLimit   = 5 * time.Second
)

func local(name string) model.Target {
	return model.Target{
		Name: name, Provider: name, Model: name + "-model", Locality: model.LocalityLocal,
		Tier: model.TierWeak, ContextWindow: 8192,
	}
}

func cloud(name string) model.Target {
	t := local(name)
	t.Locality = model.LocalityCloud
	t.Tier = model.TierStrong
	return t
}

func userRequest(text string) chat.Request {
	return chat.Request{
		ID: "req-1", Model: "auto",
		Messages: []chat.Message{
			chat.TextMessage(chat.RoleSystem, "be brief"),
			chat.TextMessage(chat.RoleUser, text),
		},
	}
}

func plan(req chat.Request, chain ...model.Target) dispatch.Plan {
	return dispatch.Plan{Request: req, Chain: chain, Localities: model.AllLocalities()}
}

// maskedPlan returns a plan whose request contains secret, with the mapping
// and detectors of mask mode.
func maskedPlan(t *testing.T, chain ...model.Target) dispatch.Plan {
	t.Helper()
	req := userRequest("mail " + secret + " please")
	det := fakedetector.NewLiteral("fake", "email", secret, secretOther)
	res, err := privacy.Scan(context.Background(), []privacy.Detector{det}, req, privacy.ScanOptions{})
	require.NoError(t, err)
	p := plan(req, chain...)
	p.Mapping = privacy.NewMapping(req, res.Findings)
	p.Detectors = []privacy.Detector{det}
	return p
}

// newMapping returns the mapping of req with one finding of typ per value.
func newMapping(req chat.Request, typ string, values ...string) *privacy.Mapping {
	fs := make([]privacy.Finding, 0, len(values))
	for _, v := range values {
		fs = append(fs, privacy.Finding{DetectorID: "test", Type: typ, Value: v, End: len(v), Confidence: 1})
	}
	return privacy.NewMapping(req, fs)
}

func newDispatcher(
	t *testing.T, providers map[string]provider.Provider, opts dispatch.Options,
) (*dispatch.Dispatcher, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(epoch)
	if opts.Rand == nil {
		opts.Rand = rand.New(rand.NewPCG(1, 2))
	}
	d, err := dispatch.New(providers, clk, opts)
	require.NoError(t, err)
	return d, clk
}

func provErr(kind error, status int, retryAfter time.Duration) error {
	return &provider.Error{Kind: kind, StatusCode: status, RetryAfter: retryAfter, Message: "scripted"}
}

var (
	errRate        = provErr(provider.ErrRateLimited, 429, 0)
	errServer      = provErr(provider.ErrUpstreamUnavailable, 500, 0)
	errUnavailable = provErr(provider.ErrUpstreamUnavailable, 0, 0)
	errTimeout     = provErr(provider.ErrTimeout, 0, 0)
	errBad         = provErr(provider.ErrBadRequest, 400, 0)
	errCtxLen      = provErr(provider.ErrContextLength, 400, 0)
)

// scripted returns a fake provider answering with the outcomes in order.
func scripted(outs ...fakeprovider.Outcome) *fakeprovider.Provider {
	return fakeprovider.New(fakeprovider.WithOutcomes(outs...))
}

// stream scripts a stream of chunks ending with err (io.EOF when nil).
func stream(chunks []chat.Chunk, err error) fakeprovider.Outcome {
	return fakeprovider.StreamChunks(chunks, err)
}

// textChunks returns a role chunk, one content chunk per part and a finish
// chunk, all with id and model.
func textChunks(modelName string, parts ...string) []chat.Chunk {
	out := []chat.Chunk{{
		ID: "id-" + modelName, Model: modelName, Created: 7,
		Choices: []chat.ChunkChoice{{Delta: chat.Delta{Role: chat.RoleAssistant}}},
	}}
	for _, p := range parts {
		out = append(out, chat.Chunk{
			ID: "id-" + modelName, Model: modelName, Created: 7,
			Choices: []chat.ChunkChoice{{Delta: chat.Delta{Content: p}}},
		})
	}
	return append(out, chat.Chunk{
		ID: "id-" + modelName, Model: modelName, Created: 7,
		Choices: []chat.ChunkChoice{{FinishReason: chat.FinishStop}},
	})
}

// cutChunks returns the role chunk and the content parts, without finish.
func cutChunks(modelName string, parts ...string) []chat.Chunk {
	c := textChunks(modelName, parts...)
	return c[:len(c)-1]
}

func usageChunk(modelName string, u chat.Usage) chat.Chunk {
	return chat.Chunk{ID: "id-" + modelName, Model: modelName, Usage: &u}
}

func response(modelName, text string, u chat.Usage) chat.Response {
	return chat.Response{
		ID: "resp-" + modelName, Model: modelName, Created: 3,
		Choices: []chat.Choice{{Message: chat.TextMessage(chat.RoleAssistant, text), FinishReason: chat.FinishStop}},
		Usage:   u,
	}
}

// sink records what the relay writes. It is used from the Stream goroutine
// and read by the test after Stream returned, or under mu.
type sink struct {
	mu       sync.Mutex
	starts   []model.Target
	chunks   []chat.Chunk
	comments []string
	errs     []error

	startErr error
	chunkErr error
	// failChunkAt makes the n-th Chunk call (1-based) fail with chunkErr.
	failChunkAt int
}

func (s *sink) Start(t model.Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, t)
	return s.startErr
}

func (s *sink) Chunk(c chat.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.starts) == 0 {
		return errors.New("sink: chunk before start")
	}
	s.chunks = append(s.chunks, c)
	if s.chunkErr != nil && len(s.chunks) >= s.failChunkAt {
		return s.chunkErr
	}
	return nil
}

func (s *sink) Comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comments = append(s.comments, text)
	return nil
}

func (s *sink) Error(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
	return nil
}

// text returns the relayed content of choice 0.
func (s *sink) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, c := range s.chunks {
		for _, ch := range c.Choices {
			b.WriteString(ch.Delta.Content)
		}
	}
	return b.String()
}

func (s *sink) chunkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.chunks)
}

// health records Report calls.
type health struct {
	mu      sync.Mutex
	reports []string
}

func (h *health) Report(target string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	status := "ok"
	if err != nil {
		status = "fail"
	}
	h.reports = append(h.reports, target+":"+status)
}

func (h *health) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.reports...)
}

// blocking is a provider whose calls block until their context ends. Streams
// first deliver chunks, then block. Every time a call starts blocking it
// signals on entered.
type blocking struct {
	chunks  []chat.Chunk
	entered chan struct{}

	mu       sync.Mutex
	requests []chat.Request
}

func newBlocking(chunks ...chat.Chunk) *blocking {
	return &blocking{chunks: chunks, entered: make(chan struct{}, 16)}
}

func (b *blocking) record(req chat.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests = append(b.requests, req.Clone())
}

func (b *blocking) Requests() []chat.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]chat.Request(nil), b.requests...)
}

func (b *blocking) Complete(ctx context.Context, req chat.Request) (chat.Response, error) {
	b.record(req)
	b.entered <- struct{}{}
	<-ctx.Done()
	return chat.Response{}, fmt.Errorf("blocking: %w", ctx.Err())
}

func (b *blocking) Stream(ctx context.Context, req chat.Request) (provider.Stream, error) {
	b.record(req)
	return &blockingStream{ctx: ctx, b: b}, nil
}

func (b *blocking) Embed(context.Context, provider.EmbedRequest) (provider.EmbedResponse, error) {
	return provider.EmbedResponse{}, errors.New("blocking: no embeddings")
}

func (b *blocking) Models(context.Context) ([]string, error) { return nil, nil }

type blockingStream struct {
	ctx context.Context //nolint:containedctx // test stream honours the context of its Stream call
	b   *blocking
	pos int
}

func (s *blockingStream) Recv() (chat.Chunk, error) {
	if s.pos < len(s.b.chunks) {
		s.pos++
		return s.b.chunks[s.pos-1], nil
	}
	s.b.entered <- struct{}{}
	<-s.ctx.Done()
	return chat.Chunk{}, fmt.Errorf("blocking: %w", s.ctx.Err())
}

func (s *blockingStream) Close() error { return nil }

func waitEntered(t *testing.T, b *blocking) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(waitLimit):
		t.Fatal("provider call did not start")
	}
}

// waitWaiters waits until the fake clock has at least n pending timers.
func waitWaiters(t *testing.T, clk *clock.Fake, n int) {
	t.Helper()
	testutil.Eventually(t, func() bool { return clk.Waiters() >= n }, waitLimit)
}

type outcome struct {
	res dispatch.Result
	err error
}

func async(f func() (dispatch.Result, error)) <-chan outcome {
	ch := make(chan outcome, 1)
	go func() {
		res, err := f()
		ch <- outcome{res: res, err: err}
	}()
	return ch
}

func await(t *testing.T, ch <-chan outcome) outcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(waitLimit):
		t.Fatal("dispatch did not return")
		return outcome{}
	}
}

func statuses(res dispatch.Result) []string {
	out := make([]string, len(res.Attempts))
	for i, a := range res.Attempts {
		out[i] = a.Target + ":" + a.Status
	}
	return out
}

// requestText returns every text part and tool-call argument of req.
func requestText(req chat.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, p := range m.Content {
			b.WriteString(p.Text)
			b.WriteByte('\n')
		}
		for _, tc := range m.ToolCalls {
			b.WriteString(tc.Function.Arguments)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
