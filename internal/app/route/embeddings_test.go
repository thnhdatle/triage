// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/testutil"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

// scriptedEmbedder returns errs in order, then a response with usage.
type scriptedEmbedder struct {
	mu    sync.Mutex
	errs  []error
	reqs  []provider.EmbedRequest
	block bool
}

func (s *scriptedEmbedder) Embed(ctx context.Context, req provider.EmbedRequest) (provider.EmbedResponse, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	var err error
	if len(s.errs) > 0 {
		err, s.errs = s.errs[0], s.errs[1:]
	}
	block := s.block
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return provider.EmbedResponse{}, context.Cause(ctx)
	}
	if err != nil {
		return provider.EmbedResponse{}, err
	}
	return provider.EmbedResponse{Model: req.Model, Vectors: [][]float32{{1}}, Usage: chat.Usage{PromptTokens: 1000}}, nil
}

func (s *scriptedEmbedder) requests() []provider.EmbedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provider.EmbedRequest(nil), s.reqs...)
}

func embedRoute(mode privacy.Mode, pool ...string) route.Route {
	r := baseRoute()
	r.Name = "embed"
	r.Pool = pool
	r.Privacy.Mode = mode
	return r
}

func embedReq(input ...string) route.EmbeddingsRequest {
	return route.EmbeddingsRequest{Request: provider.EmbedRequest{Model: "embed", Input: input, Dimensions: 8}}
}

func embedTargets() model.Pool {
	ts := allTargets()
	ts[4].Pricing.InputPerMTok = 20 // cloud-embed: 1000 tokens => 0.02
	return ts
}

func TestPipeline_Embeddings_Privacy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mode       privacy.Mode
		pool       []string
		input      string
		detErr     error
		wantTarget string
		wantErr    error
		wantCode   string
		reasons    []string
	}{
		{
			name: "off ignores findings", mode: privacy.ModeOff, pool: []string{"cloud-embed", "local-embed"},
			input: secret, wantTarget: "cloud-embed",
		},
		{
			name: "mask without finding", mode: privacy.ModeMask, pool: []string{"cloud-embed", "local-embed"},
			input: "hello", wantTarget: "cloud-embed",
		},
		{
			name: "mask finding is local_only", mode: privacy.ModeMask, pool: []string{"cloud-embed", "local-embed"},
			input: "mail " + secret, wantTarget: "local-embed", reasons: []string{"privacy:local_only"},
		},
		{
			name: "local_only finding", mode: privacy.ModeLocalOnly, pool: []string{"cloud-embed", "local-embed"},
			input: secret, wantTarget: "local-embed", reasons: []string{"privacy:local_only"},
		},
		{
			name: "detector error is local_only", mode: privacy.ModeMask, pool: []string{"cloud-embed", "local-embed"},
			input: "hello", detErr: errors.New("boom"), wantTarget: "local-embed",
			reasons: []string{"privacy:scan_error", "privacy:local_only"},
		},
		{
			name: "mask finding without local target", mode: privacy.ModeMask, pool: []string{"cloud-embed"},
			input: secret, wantErr: privacy.ErrNoLocalTarget, wantCode: "privacy_no_local_target",
			reasons: []string{"privacy:local_only"},
		},
		{
			name: "no embedding target", mode: privacy.ModeOff, pool: []string{"local-weak"},
			input: "hello", wantErr: route.ErrNoEligibleTarget, wantCode: "no_eligible_target",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cloudE, localE := &scriptedEmbedder{}, &scriptedEmbedder{}
			det := emailDetector().WithError(tc.detErr)
			e := newEnv(t, setup{
				targets: embedTargets(), routes: []route.Route{embedRoute(tc.mode, tc.pool...)},
				detectors: []privacy.Detector{det},
				embedders: map[string]route.Embedder{"cloud-embed": cloudE, "local-embed": localE},
			})
			out, err := e.p.Embeddings(context.Background(), embedReq(tc.input))
			d := e.only(t)
			assert.Equal(t, "embeddings", d.Operation)
			assert.Equal(t, tc.reasons, d.Reasons)
			assert.Equal(t, 0, e.disp.calls(), "embeddings never use the chat dispatcher")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, tc.wantCode, d.Error)
				assert.Empty(t, cloudE.requests())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTarget, out.Target)
			assert.Equal(t, tc.wantTarget+"-model", out.Model)
			assert.Equal(t, tc.wantTarget+"-model", out.Embeddings.Model)
			require.Len(t, d.Attempts, 1)
			assert.Equal(t, "ok", d.Attempts[0].Status)
			if tc.wantTarget == "local-embed" {
				assert.Empty(t, cloudE.requests(), "no input reached the cloud")
			}
		})
	}
}

func TestPipeline_Embeddings_Fallback(t *testing.T) {
	t.Parallel()
	rate := &provider.Error{Kind: provider.ErrRateLimited, StatusCode: 429}
	bad := &provider.Error{Kind: provider.ErrBadRequest, StatusCode: 400}
	tests := []struct {
		name        string
		cloudErrs   []error
		maxAttempts int
		missing     bool
		wantTarget  string
		wantStatus  []string
		wantErr     error
		wantReasons []string
	}{
		{name: "first succeeds", wantTarget: "cloud-embed", wantStatus: []string{"ok"}},
		{
			name: "retryable falls back", cloudErrs: []error{rate}, wantTarget: "local-embed",
			wantStatus: []string{"rate_limited", "ok"}, wantReasons: []string{"fallback:429"},
		},
		{
			name: "bad request stops", cloudErrs: []error{bad}, wantErr: provider.ErrBadRequest,
			wantStatus: []string{"bad_request"},
		},
		{
			name: "max attempts", cloudErrs: []error{rate}, maxAttempts: 1, wantErr: provider.ErrRateLimited,
			wantStatus: []string{"rate_limited"},
		},
		{
			name: "unknown provider skipped", missing: true, wantTarget: "local-embed",
			wantStatus: []string{"unknown_provider", "ok"}, wantReasons: []string{"fallback:unknown_provider"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cloudE, localE := &scriptedEmbedder{errs: tc.cloudErrs}, &scriptedEmbedder{}
			embedders := map[string]route.Embedder{"cloud-embed": cloudE, "local-embed": localE}
			if tc.missing {
				delete(embedders, "cloud-embed")
			}
			e := newEnv(t, setup{
				targets: embedTargets(), routes: []route.Route{embedRoute(privacy.ModeOff, "cloud-embed", "local-embed")},
				embedders: embedders, opts: route.Options{Embeddings: route.EmbeddingOptions{MaxAttempts: tc.maxAttempts}},
			})
			out, err := e.p.Embeddings(context.Background(), embedReq("hello"))
			d := e.only(t)
			got := make([]string, len(d.Attempts))
			for i, a := range d.Attempts {
				got[i] = a.Status
			}
			assert.Equal(t, tc.wantStatus, got)
			assert.Equal(t, tc.wantReasons, d.Reasons)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTarget, out.Target)
			for _, r := range append(cloudE.requests(), localE.requests()...) {
				assert.Equal(t, []string{"hello"}, r.Input)
				assert.Equal(t, 8, r.Dimensions)
			}
		})
	}
}

func TestPipeline_Embeddings_UsageAndCost(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{
		targets: embedTargets(), routes: []route.Route{embedRoute(privacy.ModeOff, "cloud-embed")},
		embedders: map[string]route.Embedder{"cloud-embed": &scriptedEmbedder{}},
	})
	_, err := e.p.Embeddings(context.Background(), embedReq("hello"))
	require.NoError(t, err)
	d := e.only(t)
	assert.Equal(t, chat.Usage{PromptTokens: 1000}, d.Usage)
	assert.InDelta(t, 0.02, d.CostUSD, 1e-12)
	assert.InDelta(t, 0.02, d.Attempts[0].CostUSD, 1e-12)
	assert.Equal(t, model.TierNone, d.Tier)
}

func TestPipeline_Embeddings_AttemptTimeout(t *testing.T) {
	t.Parallel()
	slow := &scriptedEmbedder{block: true}
	targets := embedTargets()
	targets[4].AttemptTimeout = 2 * time.Second
	e := newEnv(t, setup{
		targets: targets, routes: []route.Route{embedRoute(privacy.ModeOff, "cloud-embed", "local-embed")},
		embedders: map[string]route.Embedder{"cloud-embed": slow, "local-embed": &scriptedEmbedder{}},
	})
	done := make(chan error, 1)
	go func() {
		_, err := e.p.Embeddings(context.Background(), embedReq("hello"))
		done <- err
	}()
	testutil.Eventually(t, func() bool { return len(slow.requests()) == 1 && e.clk.Waiters() == 1 }, waitLimit)
	e.clk.Advance(2 * time.Second)
	require.NoError(t, <-done)
	d := e.only(t)
	require.Len(t, d.Attempts, 2)
	assert.Equal(t, "timeout", d.Attempts[0].Status)
	assert.Equal(t, 2*time.Second, d.Attempts[0].Latency)
	assert.Equal(t, "local-embed", d.FinalTarget())
}

func TestPipeline_Embeddings_CanceledContext(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{
		targets: embedTargets(), routes: []route.Route{embedRoute(privacy.ModeOff, "cloud-embed")},
		embedders: map[string]route.Embedder{"cloud-embed": &scriptedEmbedder{}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.p.Embeddings(ctx, embedReq("hello"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "request_canceled", e.only(t).Error)
}

func TestPipeline_Embeddings_WithFakeProvider(t *testing.T) {
	t.Parallel()
	fp := fakeprovider.New()
	e := newEnv(t, setup{
		targets: embedTargets(), routes: []route.Route{embedRoute(privacy.ModeOff, "local-embed")},
		embedders: map[string]route.Embedder{"local-embed": fp},
	})
	out, err := e.p.Embeddings(context.Background(), embedReq("a", "b"))
	require.NoError(t, err)
	assert.Len(t, out.Embeddings.Vectors, 2)
	reqs := fp.EmbedRequests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "local-embed-model", reqs[0].Model)
}
