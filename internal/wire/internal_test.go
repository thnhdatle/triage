// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/piiregex"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

func TestNewIDAt_ULIDLayout(t *testing.T) {
	t.Parallel()
	id := newIDAt(time.UnixMilli(1469922850259))
	require.Len(t, id, 26)
	assert.Equal(t, "01ARZ3NDEK", id[:10], "ULID spec time vector")
	a, b := NewID(), NewID()
	assert.NotEqual(t, a, b)
	var top [16]byte
	for i := range top {
		top[i] = 0xff
	}
	assert.Equal(t, "7ZZZZZZZZZZZZZZZZZZZZZZZZZ", encodeULID(top))
}

func TestEnv_HomeKeysAndAuth(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := Env{
		Getenv: func(k string) string { return map[string]string{"K": " v ", "EMPTY": "", "KEYS": "a, ,b"}[k] },
		Home:   func() (string, error) { return dir, nil },
	}
	p, err := env.expandHome("~/x/y")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "x/y"), p)
	p, err = env.expandHome("/abs")
	require.NoError(t, err)
	assert.Equal(t, "/abs", p)
	_, err = Env{Home: func() (string, error) { return "", errors.New("no home") }}.expandHome("~")
	require.Error(t, err)

	k, err := env.apiKey("K", "")
	require.NoError(t, err)
	assert.Equal(t, "v", k)
	_, err = env.apiKey("EMPTY", "")
	require.ErrorIs(t, err, errNoKey)
	keyFile := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyFile, []byte("secret\n"), 0o600))
	k, err = env.apiKey("", "~/key")
	require.NoError(t, err)
	assert.Equal(t, "secret", k)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "blank"), []byte("\n"), 0o600))
	_, err = env.apiKey("", filepath.Join(dir, "blank"))
	require.ErrorIs(t, err, errNoKey)
	_, err = env.apiKey("", filepath.Join(dir, "missing"))
	require.Error(t, err)
	k, err = env.apiKey("", "")
	require.NoError(t, err)
	assert.Empty(t, k)

	keys, err := env.authKeys("", "KEYS")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, keys)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keys"), []byte("# comment\nk1\n\nk2\n"), 0o600))
	keys, err = env.authKeys(filepath.Join(dir, "keys"), "")
	require.NoError(t, err)
	assert.Equal(t, []string{"k1", "k2"}, keys)
	_, err = env.authKeys("", "EMPTY")
	require.EqualError(t, err, "auth.keys_env: environment variable EMPTY is empty or not set")
	_, err = env.authKeys(filepath.Join(dir, "blank"), "")
	require.ErrorContains(t, err, "auth.keys_file "+filepath.Join(dir, "blank")+" holds no API key")
	_, err = env.authKeys("", "")
	require.ErrorContains(t, err, "no API key is configured")
	_, err = env.authKeys(filepath.Join(dir, "nope"), "")
	require.Error(t, err)
	assert.NotEmpty(t, Env{}.getenv("PATH"))
}

func TestDialable(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		":8080": "127.0.0.1:8080", "0.0.0.0:9": "127.0.0.1:9", "[::]:9": "127.0.0.1:9",
		"10.0.0.2:80": "10.0.0.2:80", "bogus": defaultDataAddr,
	} {
		assert.Equal(t, want, dialable(in), in)
	}
}

func TestRestartOnly(t *testing.T) {
	t.Parallel()
	a := config.Default()
	b := config.Default()
	assert.Empty(t, restartOnly(&a, &b))
	b.Server.Listen = "x:1"
	b.Admin.Listen = "y:1"
	b.Auth.Enabled = false
	b.ONNX.LibraryPath = "/l"
	b.Decisions.BufferSize = 1
	b.Tracing.CaptureContent = true
	assert.Len(t, restartOnly(&a, &b), 6)
}

func TestLayoutProviders_HeaderConflictGetsOwnClient(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"p": {Type: "openaicompat", BaseURL: "http://h/v1"}}
	cfg.Targets = []config.Target{
		{Name: "a", Provider: "p", Model: "m", ExtraHeaders: map[string]string{"X": "1"}},
		{Name: "b", Provider: "p", Model: "m", ExtraHeaders: map[string]string{"X": "2"}},
		{Name: "c", Provider: "p", Model: "m", ExtraHeaders: map[string]string{"X": "1"}},
		{Name: "d", Provider: "p", Model: "n", AttemptTimeout: config.Duration(5 * time.Minute)},
	}
	l, err := layoutProviders(&cfg, Env{}, "")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "p", "b": "p#b", "c": "p", "d": "p"}, l.targetClient)
	assert.Equal(t, 5*time.Minute, l.specs["p"].Timeout)
	assert.NotEqual(t, l.specs["p"].cacheKey(), l.specs["p#b"].cacheKey())

	cfg.Providers["p"] = config.Provider{Type: "openaicompat", BaseURL: "http://h/v1", APIKeyEnv: "UNSET"}
	_, err = layoutProviders(&cfg, Env{Getenv: func(string) string { return "" }}, "")
	require.Error(t, err)
}

func TestNewUpstream_Types(t *testing.T) {
	t.Parallel()
	up, err := newUpstream(clientSpec{
		Type: "anthropic", BaseURL: "http://h", Timeout: time.Second,
		Headers: map[string]map[string]string{"m": {"X": "1"}},
	})
	require.NoError(t, err)
	up.CloseIdleConnections()
	_, err = newUpstream(clientSpec{Type: "anthropic", BaseURL: "::", Timeout: time.Second})
	require.Error(t, err)
	_, err = newUpstream(clientSpec{Type: "openaicompat", BaseURL: "nope", Timeout: time.Second})
	require.Error(t, err)
}

func TestUpstreamStatus(t *testing.T) {
	t.Parallel()
	for want, err := range map[string]error{
		"ok":             nil,
		"429":            &provider.Error{Kind: provider.ErrRateLimited, StatusCode: 429},
		"canceled":       context.Canceled,
		"timeout":        provider.ErrTimeout,
		"rate_limited":   provider.ErrRateLimited,
		"context_length": provider.ErrContextLength,
		"bad_request":    provider.ErrBadRequest,
		"unavailable":    errors.New("x"),
	} {
		assert.Equal(t, want, upstreamStatus(err))
	}
}

func TestChecksAndProbes(t *testing.T) {
	t.Parallel()
	fp := fakeprovider.New()
	ts := []config.Target{
		{Name: "chat", Model: "m"},
		{Name: "emb", Model: "e", Capabilities: []string{string(model.CapEmbeddings)}},
	}
	assert.Nil(t, checksOf("models", fp, nil))
	cs := checksOf("models", fp, ts)
	require.Len(t, cs, 1)
	assert.Equal(t, []string{"chat", "emb"}, cs[0].Targets)
	cs = checksOf("completion", fp, ts)
	require.Len(t, cs, 2)
	for _, c := range cs {
		require.NoError(t, c.Probe(t.Context()))
	}
}

func TestGeneration_RefcountAndDispose(t *testing.T) {
	t.Parallel()
	closed := 0
	r := &shared{close: func(context.Context) error { closed++; return errors.New("boom") }}
	g1, g2 := newGeneration(), newGeneration()
	for _, g := range []*generation{g1, g2} {
		r.refs.Add(1)
		g.resources["r"] = r
	}
	b := &builder{log: slog.New(slog.DiscardHandler)}
	l := &live{}
	assert.Nil(t, l.swap(g1))
	held := l.acquire()
	require.Same(t, g1, held)
	b.retire(t.Context(), l.swap(g2))
	assert.Zero(t, closed, "a request still holds g1")
	held.release()
	assert.Zero(t, closed, "g2 still holds the resource")
	b.retire(t.Context(), l.swap(nil))
	assert.Equal(t, 1, closed)
	assert.Nil(t, l.acquire())
	assert.False(t, g1.tryAcquire())
}

func TestResolve_TokenizerAndMFFallback(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Models.CacheDir = t.TempDir()
	tok := filepath.Join(t.TempDir(), "tokenizer.json")
	require.NoError(t, os.WriteFile(tok, []byte(
		`{"model":{"type":"WordPiece","unk_token":"[UNK]","continuing_subword_prefix":"##",`+
			`"max_input_chars_per_word":10,"vocab":{"[UNK]":0,"a":1}}}`), 0o600))
	cfg.Selection.Tokenizer = tok
	p, err := resolve(t.Context(), &cfg, Env{})
	require.NoError(t, err)
	assert.Equal(t, tok, p.tokenizer)

	cfg.Selection.Tokenizer = "bert"
	cfg.Difficulty.MissingArtifact = "fallback"
	p, err = resolve(t.Context(), &cfg, Env{})
	require.NoError(t, err)
	assert.Empty(t, p.tokenizer)
	require.Len(t, p.warnings, 1)

	p = &plan{strategies: map[string]string{"r": strategyMF}}
	cfg.Difficulty.MF.Fallback = "bert"
	p.resolveMFFallback(&cfg, errors.New("missing"))
	assert.Equal(t, strategyLength, p.mfFallback)
	p.resolveMFFallback(&cfg, nil)
	assert.Equal(t, strategyBERT, p.mfFallback)
	assert.True(t, p.needsBERT())
}

func TestBuild_ReusesUnchangedResourcesAndReportsTokenizerErrors(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cfg, err := config.Parse([]byte(`
auth: { enabled: false }
providers: { p: { type: openaicompat, base_url: "http://127.0.0.1:1/v1" } }
targets: [{ name: a, provider: p, model: m, locality: local, tier: weak, context_window: 100 }]
routes: [{ name: r, strategy: length }]
`))
	require.NoError(t, err)
	cfg.Models.CacheDir = t.TempDir()
	ob, err := newOffline(ctx, Env{}, slog.New(slog.DiscardHandler), cfg)
	require.NoError(t, err)
	defer ob.close(ctx)
	l := &live{}
	g1, err := ob.b.build(ctx, cfg)
	require.NoError(t, err)
	ob.b.commit(ctx, l, g1)
	cfg2 := *cfg
	cfg2.Routes = []config.Route{{
		Name: "r", Strategy: "random", Threshold: 0.3, Privacy: config.RoutePrivacy{Mode: "off"},
	}}
	g2, err := ob.b.build(ctx, &cfg2)
	require.NoError(t, err)
	for k, r := range g1.resources {
		if strings.HasPrefix(k, "client:") {
			assert.Same(t, r, g2.resources[k], "provider pool reused across reloads")
		}
	}
	ob.b.commit(ctx, l, g2)

	bad := filepath.Join(t.TempDir(), "tokenizer.json")
	require.NoError(t, os.WriteFile(bad, []byte("not json"), 0o600))
	cfg3 := cfg2
	cfg3.Selection.Tokenizer = bad
	_, err = ob.b.build(ctx, &cfg3)
	require.ErrorContains(t, err, "selection.tokenizer")
	ob.b.shutdown(ctx, l)
}

func TestDetectorIDsMatchPiiregex(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Privacy.Detectors = map[string]bool{}
	for _, id := range piiregex.IDs() {
		cfg.Privacy.Detectors[id] = true
	}
	cfg.Routes = []config.Route{{Name: "r"}}
	cfg.Providers = map[string]config.Provider{"p": {Type: "openaicompat", BaseURL: "http://h/v1"}}
	cfg.Targets = []config.Target{
		{Name: "a", Provider: "p", Model: "m", Locality: "local", Tier: "weak", ContextWindow: 1},
	}
	cfg.Routes[0].Pool = []string{"a"}
	cfg.Auth = config.Auth{Enabled: true, KeysEnv: "K"}
	cfg.Routes[0].Strategy, cfg.Routes[0].DefaultTier = "none", "weak"
	cfg.Routes[0].OnTierUnavailable = "escalate"
	cfg.Routes[0].Privacy = config.RoutePrivacy{Mode: "mask", MaxClientMode: "local_only"}
	cfg.Routes[0].Threshold = 0.5
	require.NoError(t, config.Validate(&cfg), "every piiregex id is a valid privacy.detectors key")
	cfg.Privacy.Detectors["nope"] = true
	require.ErrorContains(t, config.Validate(&cfg), "privacy.detectors.nope")
}

func TestAssemblyDetectors_DisableByID(t *testing.T) {
	t.Parallel()
	text := "mail a.b@example.com now"
	for _, c := range []struct {
		dets map[string]bool
		want int
	}{{nil, 1}, {map[string]bool{"email": false}, 0}, {map[string]bool{"email": true}, 1}} {
		a := &assembly{cfg: &config.Config{Privacy: config.Privacy{Detectors: c.dets}}, p: &plan{}}
		dets, err := a.detectors()
		require.NoError(t, err)
		fs, err := dets[0].Detect(context.Background(), text)
		require.NoError(t, err)
		assert.Len(t, fs, c.want, "%v", c.dets)
	}
}
