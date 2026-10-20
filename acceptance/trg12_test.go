// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TRG-12 runs the documented quickstart commands verbatim from the repository
// root: step 1 from the README quickstart markers, step 2 from the markers in
// docs/guides/quickstart.md. `git clone` is substituted because the checkout
// under test is the working tree, not a published remote, and the local images
// built by `make docker` replace the released ones (RAYLD_TRIAGE_IMAGE,
// RAYLD_FAKELLM_IMAGE); an `export` of a variable the test sets is skipped.
// Step 2's model volume is bound to a host directory under
// ~/.cache/rayld/triage so that reruns do not download the checkpoint again.

const (
	trg12Readme      = "../README.md"
	trg12Guide       = "../docs/guides/quickstart.md"
	trg12BaseCompose = "deploy/docker-compose.yml"
	trg12BertCompose = "deploy/docker-compose.bert.yml"
	trg12CmdTimeout  = 30 * time.Minute // a cold checkpoint download on a slow link
	trg12AdminURL    = "http://127.0.0.1:9091"
	trg12ComposeName = "triage"
)

// trg12Commands returns the shell lines of the fenced block between the
// <!-- start --> and <!-- end --> markers of a Markdown file.
func trg12Commands(t *testing.T, path, start, end string) []string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed repository paths
	require.NoError(t, err)
	var cmds []string
	in, fence := false, false
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "<!-- "+start+" -->"):
			in = true
		case strings.Contains(line, "<!-- "+end+" -->"):
			in = false
		case in && strings.HasPrefix(line, "```"):
			fence = !fence
		case in && fence && strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#"):
			cmds = append(cmds, line)
		}
	}
	require.NotEmpty(t, cmds, "no commands between %s and %s in %s", start, end, path)
	return cmds
}

// trg12Run runs one documented line with bash in the repository root.
func trg12Run(t *testing.T, root string, env []string, line string) (string, time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), trg12CmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", line) //nolint:gosec // G204: documented commands
	cmd.Dir = root
	cmd.Env = env
	began := time.Now()
	out, err := cmd.CombinedOutput()
	took := time.Since(began)
	t.Logf("$ %s (%.1fs)\n%s", line, took.Seconds(), strings.TrimSpace(string(out)))
	return string(out), took, err
}

// trg12Docker runs a docker command in root, logging its output.
func trg12Docker(t *testing.T, root string, env []string, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: fixed docker arguments
	cmd.Dir = root
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("docker %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// trg12Curl parses the output of `curl -si`: the last status line (curl
// prints interim 100 responses too), its headers and the body.
type trg12Curl struct {
	status  int
	headers http.Header
	body    string
}

func trg12ParseCurl(out string) trg12Curl {
	var r trg12Curl
	blocks := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n")
	for i, b := range blocks {
		if !strings.HasPrefix(b, "HTTP/") {
			continue
		}
		lines := strings.Split(b, "\n")
		f := strings.Fields(lines[0])
		if len(f) < 2 {
			continue
		}
		r.status, _ = strconv.Atoi(f[1])
		r.headers = http.Header{}
		for _, l := range lines[1:] {
			if k, v, ok := strings.Cut(l, ":"); ok {
				r.headers.Add(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
		r.body = strings.Join(blocks[i+1:], "\n\n")
	}
	return r
}

// trg12CheckCompletion checks a quickstart curl: HTTP 200, a chat completion
// body and the three decision headers.
func trg12CheckCompletion(out string) (trg12Curl, []string) {
	r := trg12ParseCurl(out)
	var problems []string
	if r.status != http.StatusOK {
		problems = append(problems, fmt.Sprintf("status %d", r.status))
	}
	for _, h := range []string{"X-Triage-Decision", "X-Triage-Route", "X-Triage-Target"} {
		if r.headers.Get(h) == "" {
			problems = append(problems, "missing header "+h)
		}
	}
	var body struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.body)), &body); err != nil {
		problems = append(problems, "body is not JSON: "+err.Error())
	} else if body.Object != "chat.completion" || len(body.Choices) == 0 || body.Choices[0].Message.Content == "" {
		problems = append(problems, "body is not a chat completion: "+r.body)
	}
	return r, problems
}

type trg12Decision struct {
	Strategy string   `json:"strategy"`
	Variant  string   `json:"variant"`
	Score    *float64 `json:"score"`
	Tier     string   `json:"tier"`
	Reasons  []string `json:"reasons"`
}

// trg12FetchDecision reads the decision from the admin API published on host loopback.
func trg12FetchDecision(t *testing.T, id string) (trg12Decision, error) {
	t.Helper()
	var d trg12Decision
	u := trg12AdminURL + "/v1/routing-decisions/" + id
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, http.NoBody)
	if err != nil {
		return d, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return d, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return d, err
	}
	if resp.StatusCode != http.StatusOK {
		return d, fmt.Errorf("decision %s: status %d: %s", id, resp.StatusCode, data)
	}
	return d, json.Unmarshal(data, &d)
}

// trg12Env is the environment of the documented commands.
func trg12Env(t *testing.T, modelsDir string) []string {
	t.Helper()
	env := os.Environ()
	set := func(k, def string) {
		if os.Getenv(k) == "" {
			env = append(env, k+"="+def)
		}
	}
	set("RAYLD_TRIAGE_IMAGE", "thnhdatle/triage:dev")
	set("RAYLD_FAKELLM_IMAGE", "thnhdatle/fakellm:dev")
	env = append(env, "TRIAGE_MODELS_VOLUME="+modelsDir)
	return env
}

// trg12Substitute reports whether a documented line is replaced and why.
func trg12Substitute(line string, env []string) (bool, string) {
	l := strings.TrimSpace(line)
	if strings.HasPrefix(l, "git clone ") {
		return true, "the checkout under test is the local working tree"
	}
	if rest, ok := strings.CutPrefix(l, "export "); ok {
		name, _, _ := strings.Cut(rest, "=")
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				return true, name + " is set by the test (" + e + ")"
			}
		}
	}
	return false, ""
}

// trg12CachedBert reports whether the bert weights are already in the bound model directory.
func trg12CachedBert(modelsDir string) bool {
	m, _ := filepath.Glob(filepath.Join(modelsDir, ".cache/rayld/triage/models/bert/*/model.safetensors"))
	return len(m) > 0
}

// trg12State is one TRG-12 run: the commands' environment, the measurements and the problems found.
type trg12State struct {
	t         *testing.T
	root      string
	env       []string
	modelsDir string
	measured  map[string]any
	problems  []string
}

func (s *trg12State) fail(format string, args ...any) {
	s.problems = append(s.problems, fmt.Sprintf(format, args...))
}

// step1 runs the README quickstart and returns its duration and success.
func (s *trg12State) step1(lines []string) (time.Duration, bool) {
	began := time.Now()
	ok := true
	for _, line := range lines {
		if skip, why := trg12Substitute(line, s.env); skip {
			s.t.Logf("substituted: %s (%s)", line, why)
			continue
		}
		out, _, err := trg12Run(s.t, s.root, s.env, line)
		if err != nil {
			s.fail("step 1: %q: %v", line, err)
			return time.Since(began), false
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "curl ") {
			continue
		}
		r, p := trg12CheckCompletion(out)
		for _, msg := range p {
			s.fail("step 1 curl: %s", msg)
		}
		ok = ok && len(p) == 0
		s.measured["step1_decision"] = r.headers.Get("X-Triage-Decision")
		s.measured["step1_route"] = r.headers.Get("X-Triage-Route")
		s.measured["step1_target"] = r.headers.Get("X-Triage-Target")
	}
	return time.Since(began), ok
}

// step2 runs the guide's bert commands and returns the duration excluding the
// pull (the model download with its SHA-256 checks and the FP32 graph
// install), the pull duration and success.
func (s *trg12State) step2(lines []string) (time.Duration, time.Duration, bool) {
	began := time.Now()
	var download time.Duration
	ok, sawPull, sawCurl := true, false, false
	for _, line := range lines {
		out, took, err := trg12Run(s.t, s.root, s.env, line)
		if strings.Contains(line, "models pull") {
			sawPull, download = true, took
		}
		if err != nil {
			s.fail("step 2: %q: %v", line, err)
			ok = false
			break
		}
		if strings.HasPrefix(strings.TrimSpace(line), "curl ") {
			sawCurl = true
			ok = s.checkBert(out) && ok
		}
	}
	if !sawPull || !sawCurl {
		s.fail("step 2 commands must contain `models pull` and a curl")
		ok = false
	}
	return time.Since(began) - download, download, ok
}

// checkBert checks the step 2 response and its decision: strategy bert with a
// score, without on_error or a strategy fallback.
func (s *trg12State) checkBert(out string) bool {
	r, p := trg12CheckCompletion(out)
	for _, msg := range p {
		s.fail("step 2 curl: %s", msg)
	}
	s.measured["step2_route_reason"] = r.headers.Get("X-Triage-Route-Reason")
	id := r.headers.Get("X-Triage-Decision")
	if id == "" {
		return false
	}
	d, err := trg12FetchDecision(s.t, id)
	if err != nil {
		s.fail("step 2 decision: %v", err)
		return false
	}
	s.measured["step2_strategy"] = d.Strategy
	s.measured["step2_variant"] = d.Variant
	s.measured["step2_tier"] = d.Tier
	s.measured["step2_reasons"] = d.Reasons
	ok := len(p) == 0
	if d.Score != nil {
		s.measured["step2_score"] = *d.Score
	}
	if d.Strategy != "bert" || d.Score == nil {
		s.fail("step 2 decision: strategy %q, score %v, want bert with a score", d.Strategy, d.Score)
		ok = false
	}
	for _, reason := range d.Reasons {
		if strings.Contains(reason, "error->") || strings.Contains(reason, "->length") {
			s.fail("step 2 decision took on_error or a fallback: %s", reason)
			ok = false
		}
	}
	return ok
}

// trg12Stack stops the runner's stack (same compose project and ports) so
// step 1 starts from nothing, and registers the cleanup that removes what the
// test started and brings the runner's stack back.
func trg12Stack(t *testing.T, root string, env []string) {
	t.Helper()
	ps := exec.CommandContext(t.Context(), "docker", "compose", "-f", trg12BaseCompose, "ps", "-q") //nolint:gosec // fixed
	ps.Dir = root
	psOut, psErr := ps.Output()
	running := psErr == nil && strings.TrimSpace(string(psOut)) != ""
	t.Cleanup(func() {
		_ = trg12Docker(t, root, env, "compose", "-f", trg12BaseCompose, "-f", trg12BertCompose, "down", "--remove-orphans")
		// The named volume exists only if TRIAGE_MODELS_VOLUME was not honoured.
		_ = trg12Docker(t, root, env, "volume", "rm", "-f", trg12ComposeName+"_triage-models")
		if running {
			_ = trg12Docker(t, root, env, "compose", "-f", trg12BaseCompose, "up", "-d", "--wait")
		}
	})
	require.NoError(t, trg12Docker(t, root, env, "compose", "-f", trg12BaseCompose, "down", "--remove-orphans"))
}

// TestAcceptance_TRG12_Quickstart implements TRG-12.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG12_Quickstart(t *testing.T) {
	th := thresholds(t, "TRG-12")
	step1Max := time.Duration(num(t, th, "step1_max_minutes") * float64(time.Minute))
	step2Max := time.Duration(num(t, th, "step2_max_minutes") * float64(time.Minute))

	root, err := filepath.Abs("..")
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	modelsDir := filepath.Join(home, ".cache", "rayld", "triage", "quickstart-home")
	require.NoError(t, os.MkdirAll(modelsDir, 0o750))
	s := &trg12State{t: t, root: root, env: trg12Env(t, modelsDir), modelsDir: modelsDir, measured: map[string]any{}}

	step1 := trg12Commands(t, trg12Readme, "quickstart:start", "quickstart:end")
	step2 := trg12Commands(t, trg12Guide, "quickstart-bert:start", "quickstart-bert:end")
	trg12Stack(t, root, s.env)

	step1Took, step1OK := s.step1(step1)
	s.measured["step1_seconds"] = step1Took.Seconds()
	s.measured["step1_success"] = step1OK
	if step1Took >= step1Max {
		s.fail("step 1 took %s (limit %s)", step1Took, step1Max)
	}

	cached := trg12CachedBert(modelsDir)
	s.measured["download_cached_before"] = cached
	step2Took, download, step2OK := s.step2(step2)
	s.measured["download_seconds"] = download.Seconds()
	s.measured["download_cached_after"] = trg12CachedBert(modelsDir)
	s.measured["step2_seconds_excluding_download"] = step2Took.Seconds()
	s.measured["step2_success"] = step2OK
	if step2Took >= step2Max {
		s.fail("step 2 took %s excluding download (limit %s)", step2Took, step2Max)
	}

	pass := step1OK && step2OK && len(s.problems) == 0
	details := fmt.Sprintf("step 1 %.1fs, step 2 %.1fs excluding download, download %.1fs (checkpoint cached before: %v)",
		step1Took.Seconds(), step2Took.Seconds(), download.Seconds(), cached)
	if !pass {
		details += "; " + strings.Join(s.problems, "; ")
	}
	writeResult(t, Result{ID: "TRG-12", Measured: s.measured, Pass: pass, Details: details})
	require.True(t, pass, details)
}
