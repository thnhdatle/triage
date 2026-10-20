// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const reqLMStudio = "lmstudio"

const (
	minFreeBytes    = 18 << 30
	probeTimeout    = 5 * time.Second
	defaultLMStudio = "http://localhost:1234/v1"
)

// probeResult is the outcome of one `requires` probe.
type probeResult struct {
	OK     bool
	Detail string
}

// prober probes environment capabilities; fields are overridable for tests.
type prober struct {
	ci     bool
	goos   string
	goarch string
	models []string
}

func newProber(ci bool) *prober {
	return &prober{ci: ci, goos: runtime.GOOS, goarch: runtime.GOARCH, models: lmstudioModels()}
}

// LM Studio settings of the cost-quality criterion. The same variables and
// defaults are read by the test (acceptance/trg03_test.go).
const (
	envLMStudioURL     = "TRIAGE_LMSTUDIO_BASE_URL"
	envWeakModel       = "TRIAGE_TRG03_WEAK_MODEL"
	envStrongModel     = "TRIAGE_TRG03_STRONG_MODEL"
	defaultWeakModel   = "qwen/qwen3-vl-4b"
	defaultStrongModel = "gemma-4-26b-a4b-it-qat-mlx"
)

// envOr returns the trimmed value of key, or def when it is unset or blank.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// lmstudioModels returns the weak and strong model ids that must be loaded in LM Studio.
func lmstudioModels() []string {
	return []string{envOr(envWeakModel, defaultWeakModel), envOr(envStrongModel, defaultStrongModel)}
}

// isBlocking reports whether a missing requirement is "Blocked" rather than "Skipped".
func isBlocking(req string) bool {
	switch req {
	case "docker", "python", "node", "mac":
		return true
	}
	return false
}

func commandProbe(name string, args ...string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // fixed tool names
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if err != nil {
		return probeResult{Detail: fmt.Sprintf("`%s` failed: %v", cmd, err)}
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return probeResult{OK: true, Detail: fmt.Sprintf("`%s` ok: %s", cmd, first)}
}

func httpGet(url string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		buf.WriteString(sc.Text())
	}
	return resp.StatusCode, []byte(buf.String()), nil
}

func (p *prober) macProbe() probeResult {
	if p.goos == "darwin" && p.goarch == "arm64" {
		return probeResult{OK: true, Detail: "darwin/arm64 reference host"}
	}
	if p.ci {
		return probeResult{OK: true, Detail: fmt.Sprintf("%s/%s with CI=1: CI thresholds apply", p.goos, p.goarch)}
	}
	return probeResult{Detail: fmt.Sprintf("host is %s/%s, not darwin/arm64", p.goos, p.goarch)}
}

func networkProbe() probeResult {
	status, _, err := httpGet("https://huggingface.co")
	if err != nil {
		return probeResult{Detail: err.Error()}
	}
	return probeResult{OK: true, Detail: fmt.Sprintf("GET https://huggingface.co -> %d", status)}
}

func lmstudioURL() string {
	return strings.TrimRight(envOr(envLMStudioURL, defaultLMStudio), "/")
}

func (p *prober) lmstudioProbe() probeResult {
	base := lmstudioURL()
	status, body, err := httpGet(base + "/models")
	if err != nil {
		return probeResult{Detail: err.Error()}
	}
	if status != http.StatusOK {
		return probeResult{Detail: fmt.Sprintf("GET %s/models -> %d", base, status)}
	}
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return probeResult{Detail: fmt.Sprintf("GET %s/models: bad JSON: %v", base, err)}
	}
	have := map[string]bool{}
	for _, m := range doc.Data {
		have[m.ID] = true
	}
	for _, id := range p.models {
		if !have[id] {
			return probeResult{Detail: fmt.Sprintf("model %q not listed at %s/models", id, base)}
		}
	}
	free, err := freeMemory()
	if err != nil {
		return probeResult{Detail: err.Error()}
	}
	if free < minFreeBytes {
		return probeResult{Detail: fmt.Sprintf("free memory %.1f GB < 18 GB", float64(free)/(1<<30))}
	}
	return probeResult{OK: true, Detail: fmt.Sprintf("models present, free memory %.1f GB", float64(free)/(1<<30))}
}

// probe runs the probe for one requires entry.
func (p *prober) probe(req string) probeResult {
	switch req {
	case "docker":
		return commandProbe("docker", "info", "--format", "{{.ServerVersion}}")
	case "python":
		return commandProbe("python3.12", "--version")
	case "node":
		return commandProbe("node", "--version")
	case "network":
		return networkProbe()
	case "mac":
		return p.macProbe()
	case reqLMStudio:
		return p.lmstudioProbe()
	}
	return probeResult{Detail: fmt.Sprintf("unknown requirement %q", req)}
}

var (
	pageSizeRe = regexp.MustCompile(`page size of (\d+) bytes`)
	pagesRe    = regexp.MustCompile(`^Pages (free|inactive|speculative):\s+(\d+)\.`)
)

// parseVMStat returns free+inactive+speculative bytes from vm_stat output.
func parseVMStat(out string) (uint64, error) {
	pageSize := uint64(16384)
	if m := pageSizeRe.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseUint(m[1], 10, 64); err == nil {
			pageSize = v
		}
	}
	var pages uint64
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		m := pagesRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, err := strconv.ParseUint(m[2], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse vm_stat line %q: %w", line, err)
		}
		pages += v
		seen++
	}
	if seen != 3 {
		return 0, errors.New("vm_stat output lacks free/inactive/speculative pages")
	}
	return pages * pageSize, nil
}

func freeMemory() (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("vm_stat: %w", err)
	}
	return parseVMStat(string(out))
}
