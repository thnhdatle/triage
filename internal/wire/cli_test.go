// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/wire"
)

type cliResult struct {
	code           int
	stdout, stderr string
}

func runCLI(t *testing.T, env map[string]string, args ...string) cliResult {
	t.Helper()
	var out, errb bytes.Buffer
	c := &wire.CLI{Stdout: &out, Stderr: &errb, Env: wire.Env{
		Getenv: func(k string) string { return env[k] },
		Home:   func() (string, error) { return t.TempDir(), nil },
	}}
	code := c.Run(t.Context(), args)
	return cliResult{code: code, stdout: out.String(), stderr: errb.String()}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func offlineConfig(extra string) string {
	return `
auth: { enabled: false }
providers:
  fake: { type: openaicompat, base_url: "http://127.0.0.1:1/v1", api_key_env: FAKE_KEY }
targets:
  - { name: small, provider: fake, model: fake-small, locality: local, tier: weak, context_window: 4096 }
  - { name: big, provider: fake, model: fake-strong, locality: cloud, tier: strong, context_window: 4096,
      extra_headers: { Authorization-Extra: secret-value } }
` + extra
}

const lengthRoute = `
routes:
  - { name: auto, pool: [small, big], strategy: length, threshold: 0.5 }
  - { name: rules, pool: [small, big], strategy: none }
`

func TestCLI_UsageAndVersion(t *testing.T) {
	t.Parallel()
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil).code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "frobnicate").code)
	assert.Equal(t, wire.ExitOK, runCLI(t, nil, "help").code)
	r := runCLI(t, nil, "version")
	assert.Equal(t, wire.ExitOK, r.code)
	assert.True(t, strings.HasPrefix(r.stdout, "triage "))
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "serve").code, "serve needs a config")
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "serve", "--bogus").code)
	assert.Equal(t, wire.ExitOK, runCLI(t, nil, "serve", "-h").code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "serve", "--config", "/nonexistent/triage.yaml").code)
}

func TestCLI_ConfigValidateAndPrint(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FAKE_KEY": "k"}
	good := writeFile(t, "good.yaml", offlineConfig(lengthRoute))
	r := runCLI(t, env, "config", "validate", "--config", good)
	assert.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stdout, ": ok")

	r = runCLI(t, map[string]string{"FAKE_KEY": "k", wire.EnvConfig: good}, "config", "print", "--redact")
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "[redacted]")
	assert.NotContains(t, r.stdout, "secret-value")
	r = runCLI(t, env, "config", "print", "--config", good)
	assert.Contains(t, r.stdout, "secret-value")

	bad := writeFile(t, "bad.yaml", offlineConfig(lengthRoute)+"nonsense: true\n")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "config", "validate", "--config", bad).code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "config", "validate").code, "no config")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "config").code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "config", "explain", "--config", good).code)
}

func TestCLI_ConfigValidate_ArtifactsAndEgress(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FAKE_KEY": "k"}
	cache := t.TempDir()
	bertRoute := "models: { cache_dir: " + cache + " }\n" +
		"routes:\n  - { name: learned, pool: [small, big], strategy: bert }\n"
	p := writeFile(t, "bert.yaml", offlineConfig(bertRoute))
	r := runCLI(t, env, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitUsage, r.code)
	assertBERTUnavailable(t, r.stderr)

	p = writeFile(t, "fallback.yaml", offlineConfig(bertRoute+"difficulty: { missing_artifact: fallback }\n"))
	r = runCLI(t, env, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "using length")

	mfRoute := "models: { cache_dir: " + cache + " }\n" +
		"routes:\n  - { name: mfr, pool: [small, big], strategy: mf }\n" +
		"difficulty: { missing_artifact: fallback, mf: { provider: fake } }\n"
	p = writeFile(t, "mf.yaml", offlineConfig(mfRoute))
	r = runCLI(t, env, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitUsage, r.code)
	assert.Contains(t, r.stderr, "data egress")

	p = writeFile(t, "mf2.yaml", offlineConfig(strings.Replace(mfRoute, "mf: { provider: fake }",
		"mf: { provider: fake, allow_external_embedding: true }", 1)))
	r = runCLI(t, nil, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitUsage, r.code, "no API key in the environment")
	assert.Contains(t, r.stderr, "API key")
	r = runCLI(t, env, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "mf scoring cost is recorded as 0")

	p = writeFile(t, "ner.yaml", offlineConfig(lengthRoute+"models: { cache_dir: "+cache+" }\n"+
		"privacy: { ner: { enabled: true } }\n"))
	r = runCLI(t, env, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitUsage, r.code)
	assert.Contains(t, r.stderr, "privacy.ner")

	p = writeFile(t, "tok.yaml", offlineConfig(lengthRoute+"selection: { tokenizer: /nonexistent/tokenizer.json }\n"))
	r = runCLI(t, env, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitUsage, r.code)
	assert.Contains(t, r.stderr, "selection.tokenizer")

	unsupported := writeFile(t, "tokenizer.json", `{"model":{"type":"Foo"}}`)
	p = writeFile(t, "tok2.yaml", offlineConfig(lengthRoute+"selection: { tokenizer: "+unsupported+" }\n"))
	r = runCLI(t, env, "config", "validate", "--config", p)
	assert.Equal(t, wire.ExitUsage, r.code, "an unsupported tokenizer.json fails validation, not only startup")
	assert.Contains(t, r.stderr, "selection.tokenizer")
}

func TestCLI_Healthcheck(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	tr := startTriage(t, baseConfig(f, defaultRoutes))
	assert.Equal(t, wire.ExitOK, runCLI(t, nil, "healthcheck", "--url", tr.data+"/readyz").code)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	cfg := writeFile(t, "hc.yaml", offlineConfig(lengthRoute+`server: { listen: "`+addr+`" }`+"\n"))
	r := runCLI(t, map[string]string{"FAKE_KEY": "k"}, "healthcheck", "--config", cfg)
	assert.Equal(t, wire.ExitFailure, r.code)
	assert.Contains(t, r.stderr, addr)
	assert.Equal(t, wire.ExitFailure, runCLI(t, nil, "healthcheck", "--url", tr.data+"/nope").code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "healthcheck", "--url", "::bad").code)
}

func TestCLI_ModelsListAndPullUsage(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	r := runCLI(t, nil, "models", "list", "--cache-dir", cache)
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	for _, name := range []string{"bert", "mf", "ner", "0/4 verified", "onnx-fp32:missing"} {
		assert.Contains(t, r.stdout, name)
	}
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "models").code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "models", "pull", "--cache-dir", cache).code)
	cfg := writeFile(t, "c.yaml", offlineConfig(lengthRoute+"models: { cache_dir: "+cache+" }\n"))
	r = runCLI(t, map[string]string{"FAKE_KEY": "k"}, "models", "pull", "--config", cfg)
	assert.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "needs no model artifacts")
	r = runCLI(t, nil, "models", "pull", "--cache-dir", cache, "unknown-entry")
	assert.Equal(t, wire.ExitFailure, r.code)
	assert.Contains(t, r.stderr, "unknown")
}

func TestCLI_Calibrate(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FAKE_KEY": "k"}
	cfg := writeFile(t, "c.yaml", offlineConfig(lengthRoute))
	lines := make([]string, 0, 11)
	for i := range 10 {
		lines = append(lines, `{"prompt": "`+strings.Repeat("word ", 400*i+1)+`"}`)
	}
	lines = append(lines, `{"id":"m","messages":[{"role":"system","content":"s"},`+
		`{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url"}]}]}`)
	sample := writeFile(t, "sample.jsonl", strings.Join(lines, "\n")+"\n\n")
	r := runCLI(t, env, "calibrate", "--config", cfg, "--route", "auto", "--strong-ratio", "0.3", "--sample", sample)
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "name: auto")
	assert.Contains(t, r.stdout, "threshold:")
	assert.Contains(t, r.stderr, "strategy length on 11 requests")

	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "calibrate", "--config", cfg).code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "calibrate", "--config", cfg, "--route", "rules",
		"--strong-ratio", "0.3", "--sample", sample).code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "calibrate", "--config", cfg, "--route", "nope",
		"--strong-ratio", "0.3", "--sample", sample).code)
	bad := writeFile(t, "bad.jsonl", "{not json}\n")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "calibrate", "--config", cfg, "--route", "auto",
		"--strong-ratio", "0.3", "--sample", bad).code)
}

func TestCLI_EvalLabels(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FAKE_KEY": "k"}
	cfg := writeFile(t, "c.yaml", offlineConfig(lengthRoute))
	lines := make([]string, 0, 40)
	for i := range 40 {
		weak := i%3 != 0
		strong := i%5 != 1
		lines = append(lines, `{"id":"q`+string(rune('a'+i%26))+string(rune('a'+i/26))+`","prompt":"`+
			strings.Repeat("token ", 50*i+1)+`","weak_correct":`+boolStr(weak)+`,"strong_correct":`+boolStr(strong)+`}`)
	}
	data := writeFile(t, "labels.jsonl", strings.Join(lines, "\n"))
	out := filepath.Join(t.TempDir(), "run")
	ev := writeFile(t, "eval.yaml", `
mode: labels
config: `+cfg+`
route: auto
dataset: { jsonl: `+data+` }
strategies: [random, length, mf]
threshold_rule: { calibration: `+data+` }
bootstrap: { resamples: 200, seed: 7 }
`)
	r := runCLI(t, env, "eval", "--config", ev, "--out", out)
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	summary, err := os.ReadFile(filepath.Join(out, "summary.md"))
	require.NoError(t, err)
	assert.Contains(t, string(summary), "| random |")
	assert.Contains(t, string(summary), "| length |")
	assert.Contains(t, string(summary), "not measured")
	csv, err := os.ReadFile(filepath.Join(out, "results.csv"))
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(csv)), "\n"), 1+2*40)

	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval").code)
	bad := writeFile(t, "bad.yaml", "mode: sometimes\n")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval", "--config", bad).code)
	nolabel := writeFile(t, "nl.jsonl", `{"prompt":"x"}`)
	ev2 := writeFile(t, "eval2.yaml", "mode: labels\nconfig: "+cfg+"\nroute: auto\ndataset: { jsonl: "+nolabel+
		" }\nstrategies: [length]\n")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval", "--config", ev2, "--out", t.TempDir()).code)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestCLI_EvalLive_GeneratesAndCaches(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	cfg := writeFile(t, "c.yaml", baseConfig(f, defaultRoutes))
	lines := make([]string, 0, 6)
	for i := range 6 {
		p := "question " + strings.Repeat("x", i+1)
		lines = append(lines, `{"id":"i`+string(rune('0'+i))+`","prompt":"`+p+`","reference":"`+p+`","scorer":"exact"}`)
	}
	data := writeFile(t, "live.jsonl", strings.Join(lines, "\n"))
	cache := t.TempDir()
	ev := writeFile(t, "eval.yaml", `
mode: live
config: `+cfg+`
route: triage-auto
dataset: { jsonl: `+data+` }
targets:
  weak: { provider: fake, model: fake-small }
  strong: { provider: fake, model: fake-strong, pricing: { input_per_mtok: 1.25, output_per_mtok: 10 } }
generation: { temperature: 0, seed: 7, max_tokens: 64 }
strategies: [random, length]
bootstrap: { resamples: 100, seed: 7 }
cache_dir: `+cache+`
`)
	f.script(t, `[{"model":"fake-strong","scenario":"echo","count":6}]`)
	r := runCLI(t, nil, "eval", "--config", ev, "--out", filepath.Join(t.TempDir(), "a"))
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "| length |")
	before := len(f.requests(t))
	assert.Equal(t, 12, before, "6 items x 2 tiers, one batch per model")
	r = runCLI(t, nil, "eval", "--config", ev, "--out", filepath.Join(t.TempDir(), "b"))
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Len(t, f.requests(t), before, "second run answers from the cache")
	assert.Contains(t, r.stdout, "| length | 0.5000 | 0.0% | 0.000 (", "short prompts stay weak; weak answers are wrong")
}
