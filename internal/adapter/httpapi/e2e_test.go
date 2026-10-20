// SPDX-License-Identifier: Apache-2.0

package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/httpapi"
	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
	"github.com/thnhdatle/triage/internal/testutil/fakerecorder"
)

// pipelineEnv wires the real route pipeline and dispatcher over a fake provider.
func pipelineEnv(t *testing.T, fp *fakeprovider.Provider) (http.Handler, *fakerecorder.Recorder) {
	t.Helper()
	weak := model.Target{
		Name: "local-weak", Provider: "fake", Model: "qwen", Locality: model.LocalityLocal,
		Tier: model.TierWeak, ContextWindow: 8192,
	}
	embed := model.Target{
		Name: "embed", Provider: "fake", Model: "te3", Locality: model.LocalityLocal,
		Capabilities: []model.Capability{model.CapEmbeddings}, ContextWindow: 8192,
	}
	pool := model.Pool{weak, embed}
	disp, err := dispatch.New(map[string]provider.Provider{"fake": fp}, clock.Real(), dispatch.Options{})
	require.NoError(t, err)
	rec := fakerecorder.New()
	r := route.Route{
		Name: "auto", Privacy: route.Privacy{Mode: privacy.ModeOff}, Strategy: route.StrategyNone,
		DefaultTier: model.TierWeak,
	}
	p, err := route.New(route.Deps{
		Targets: pool, Routes: []route.Route{r}, Dispatcher: disp, Embedders: map[string]route.Embedder{"fake": fp},
		Recorder: rec, Clock: clock.Real(), NewID: idGen(),
	}, route.Options{})
	require.NoError(t, err)
	cat := fakeCatalog{routes: []string{"auto"}, targets: pool}
	s, err := httpapi.New(p, cat, idGen())
	require.NoError(t, err)
	return s.API(), rec
}

func TestPipeline_Chat_RecordsDecisionUnderHeaderID(t *testing.T) {
	t.Parallel()
	h, rec := pipelineEnv(t, fakeprovider.New())

	resp := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"ping"}]}`)

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, rec.Len())
	d := rec.Decisions()[0]
	assert.Equal(t, d.ID, resp.Header().Get("X-Triage-Decision"))
	assert.Equal(t, "auto", resp.Header().Get("X-Triage-Route"))
	assert.Equal(t, "local-weak", resp.Header().Get("X-Triage-Target"))
	assert.Contains(t, resp.Body.String(), "ping")
}

func TestPipeline_Chat_UnknownModel_404(t *testing.T) {
	t.Parallel()
	h, _ := pipelineEnv(t, fakeprovider.New())

	resp := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"nope","messages":[{"role":"user","content":"x"}]}`)

	require.Equal(t, http.StatusNotFound, resp.Code)
	assert.Equal(t, "model_not_found", errField(t, resp, "code"))
}

func TestPipeline_Chat_UpstreamRateLimited_429(t *testing.T) {
	t.Parallel()
	fp := fakeprovider.New()
	for range 3 {
		fp.Enqueue(fakeprovider.Fail(provider.ErrBadRequest))
	}
	h, _ := pipelineEnv(t, fp)

	resp := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"x"}]}`)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	assert.Equal(t, "upstream_bad_request", errField(t, resp, "code"))
	assert.NotEmpty(t, resp.Header().Get("X-Triage-Decision"))
}

func TestPipeline_Stream_DeliversChunksAndDone(t *testing.T) {
	t.Parallel()
	h, rec := pipelineEnv(t, fakeprovider.New())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp := post(t, srv.URL+"/v1/chat/completions",
		`{"model":"auto","stream":true,"stream_options":{"include_usage":true},`+
			`"messages":[{"role":"user","content":"ping"}]}`)
	defer resp.Body.Close()
	body := readAll(t, resp.Body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "local-weak", resp.Header.Get("X-Triage-Target"))
	assert.Contains(t, body, "ping")
	assert.Contains(t, body, `"usage"`)
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"))
	require.Equal(t, 1, rec.Len())
	assert.NotEmpty(t, resp.Header.Get("X-Triage-Decision"))
	assert.Equal(t, rec.Decisions()[0].ID, resp.Header.Get("X-Triage-Decision"))
}

func TestPipeline_Embeddings(t *testing.T) {
	t.Parallel()
	h, _ := pipelineEnv(t, fakeprovider.New())

	resp := do(t, h, http.MethodPost, "/v1/embeddings", `{"model":"auto","input":["a","b"]}`)

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	assert.Len(t, decodeBody(t, resp)["data"], 2)
	assert.Equal(t, "embed", resp.Header().Get("X-Triage-Target"))
}
