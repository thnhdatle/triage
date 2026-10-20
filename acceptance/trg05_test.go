// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	trg05Key     = "trg05-secret-key"
	trg05Timeout = 5 * time.Minute
)

// trg05Config serves five routes over fakellm: a chat route, a tool route whose
// target makes fakellm answer with a tool call, and an embeddings route.
func trg05Config(f *fake) string {
	return `
server: { listen: "127.0.0.1:0" }
admin: { listen: "127.0.0.1:0" }
auth: { enabled: true, keys_env: TRG05_KEYS }
providers:
  fake: { type: openaicompat, base_url: "` + f.baseURL() + `" }
health: { interval: 1h }
targets:
  - { name: chat-target, provider: fake, model: trg05-chat, locality: cloud, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode],
      pricing: { input_per_mtok: 1, output_per_mtok: 2 } }
  - { name: tool-target, provider: fake, model: trg05-tools, locality: cloud, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode],
      extra_headers: { X-Fake-Scenario: tool-call },
      pricing: { input_per_mtok: 1, output_per_mtok: 2 } }
  - { name: embed-target, provider: fake, model: trg05-embed, locality: cloud,
      capabilities: [embeddings], pricing: { input_per_mtok: 0.02, output_per_mtok: 0 } }
routes:
  - { name: trg05-chat, pool: [chat-target], strategy: none, default_tier: weak, privacy: { mode: off } }
  - { name: trg05-tools, pool: [tool-target], strategy: none, default_tier: weak, privacy: { mode: off } }
  - { name: trg05-embed, pool: [embed-target], privacy: { mode: off } }
`
}

type sdkLine struct {
	Check   string `json:"check"`
	OK      bool   `json:"ok"`
	Detail  any    `json:"detail"`
	Summary bool   `json:"summary"`
}

// runSDK runs one SDK script and returns its parsed check lines and output.
func runSDK(t *testing.T, env []string, dir, name string, args ...string) ([]sdkLine, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), trg05Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: fixed SDK runner commands
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s %v: %v", name, args, err)
	}
	var lines []sdkLine
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var l sdkLine
		if json.Unmarshal(sc.Bytes(), &l) == nil && (l.Check != "" || l.Summary) {
			lines = append(lines, l)
		}
	}
	return lines, string(out)
}

// TestAcceptance_TRG05_SDKCompat implements TRG-05.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG05_SDKCompat(t *testing.T) {
	th := thresholds(t, "TRG-05")
	minRatio := num(t, th, "script_checks_passed_ratio")
	f := composeFake(t)
	t.Setenv("TRG05_KEYS", trg05Key)
	tr := startTriage(t, trg05Config(f))

	env := []string{
		"OPENAI_BASE_URL=" + tr.data + "/v1",
		"OPENAI_API_KEY=" + trg05Key,
		"CHECK_CHAT_MODEL=trg05-chat",
		"CHECK_TOOL_MODEL=trg05-tools",
		"CHECK_EMBED_MODEL=trg05-embed",
		"CHECK_UNKNOWN_MODEL=trg05-unknown",
		"CHECK_LIST_MODELS=trg05-chat,trg05-tools,trg05-embed",
	}
	sdk, err := filepath.Abs("sdk")
	require.NoError(t, err)

	// The Node install runs in place; node_modules is gitignored.
	nodeDir := filepath.Join(sdk, "node")
	_, out := runSDK(t, env, nodeDir, "npm", "ci", "--no-audit", "--no-fund")
	t.Logf("npm ci: %s", strings.TrimSpace(out))

	py, pyOut := runSDK(t, env, filepath.Join(sdk, "python"), "uv", "run", "--frozen", "python", "check.py")
	nd, ndOut := runSDK(t, env, nodeDir, "node", "check.mjs")

	var total, passed int
	var failed []string
	for _, l := range append(py, nd...) {
		if l.Summary {
			continue
		}
		total++
		if l.OK {
			passed++
		} else {
			failed = append(failed, l.Check)
			t.Logf("FAILED %s: %v", l.Check, l.Detail)
		}
	}
	// 7 checks per SDK; a script that crashed early must not pass by omission.
	const wantChecks = 14
	ratio := 0.0
	if total > 0 {
		ratio = float64(passed) / float64(max(total, wantChecks))
	}
	pass := ratio >= minRatio && total == wantChecks
	details := "all SDK checks passed"
	if !pass {
		details = "failed checks: " + strings.Join(failed, ",") + "; python output: " + pyOut + "; node output: " + ndOut
	}
	writeResult(t, Result{
		ID: "TRG-05",
		Measured: map[string]any{
			"script_checks_passed_ratio": ratio, "checks_total": total, "checks_passed": passed,
		},
		Pass: pass, Details: details,
	})
	require.True(t, pass, details)
}
