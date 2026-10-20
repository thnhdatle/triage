// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/acceptance"
)

func testFile() *acceptance.File {
	return &acceptance.File{Project: "p", Criteria: []acceptance.Criterion{
		{ID: "A-1", Test: "TestAcceptance_A1_X", Requires: []string{"lmstudio"}},
		{ID: "A-2", Test: "BenchmarkAcceptance_A2_Y"},
	}}
}

func TestCheckCriteria(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sources map[string]string
		want    int
	}{
		{"all present", map[string]string{
			"a_test.go": "func TestAcceptance_A1_X(t *testing.T) {}\nfunc BenchmarkAcceptance_A2_Y(b *testing.B) {}\n",
		}, 0},
		{"missing", map[string]string{"a_test.go": "func TestAcceptance_A1_X(t *testing.T) {}\n"}, 1},
		{"duplicate", map[string]string{
			"a_test.go": "func TestAcceptance_A1_X(t *testing.T) {}\nfunc BenchmarkAcceptance_A2_Y(b *testing.B) {}\n",
			"b_test.go": "func TestAcceptance_A1_X(t *testing.T) {}\n",
		}, 1},
		{"stray acceptance test", map[string]string{
			"a_test.go": "func TestAcceptance_A1_X(t *testing.T) {}\nfunc BenchmarkAcceptance_A2_Y(b *testing.B) {}\n" +
				"func TestAcceptance_Z9_Other(t *testing.T) {}\n",
		}, 1},
		{"non-test file ignored", map[string]string{
			"a.go": "func TestAcceptance_A1_X(t *testing.T) {}\nfunc BenchmarkAcceptance_A2_Y(b *testing.B) {}\n",
		}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Len(t, checkCriteria(testFile(), tc.sources), tc.want)
		})
	}
}

func TestLoadSources_SkipsThirdPartyDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"a_test.go":                          "ok",
		"sdk/python/check.py":                "ok",
		"sdk/python/.venv/lib/x/test_x.py":   "pytest.skip",
		"sdk/node/node_modules/pkg/index.py": "xfail",
		"logs/TRG-01.txt":                    "ignored",
		"cmd/runner/main.go":                 "ignored",
	}
	for p, c := range files {
		full := filepath.Join(dir, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(c), 0o600))
	}
	got, err := loadSources(dir)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Contains(t, got, filepath.Join(dir, "a_test.go"))
	assert.Contains(t, got, filepath.Join(dir, "sdk/python/check.py"))
}

func TestScanSkips(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"clean", "func TestX(t *testing.T) { t.Fatal(1) }", 0},
		{"t.Skip", "func TestX(t *testing.T) {\n\tt.Skip(\"no\")\n}", 1},
		{"Skipf", "t.Skipf(\"%s\", x)", 1},
		{"SkipNow", "t.SkipNow()", 1},
		{"short", "if testing.Short() {}", 1},
		{"pytest", "@pytest.mark.skip\npytest.skip('x')", 2},
		{"xfail", "# xfail", 1},
		{"field named Skip is fine", "th.Skip = true", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Len(t, scanSkips(map[string]string{"f.go": tc.src}), tc.want)
		})
	}
}

func TestSelectCriteria(t *testing.T) {
	t.Parallel()
	f := testFile()
	all, err := selectCriteria(f, "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "A-2", all[0].ID, "lmstudio criteria run last")
	one, err := selectCriteria(f, " A-1 ")
	require.NoError(t, err)
	assert.Len(t, one, 1)
	_, err = selectCriteria(f, "A-9")
	require.Error(t, err)
}

func TestThresholdSelection(t *testing.T) {
	t.Parallel()
	c := acceptance.Criterion{Pass: acceptance.Pass{
		Mac: acceptance.Threshold{Values: map[string]any{"n": 1}},
		CI:  acceptance.Threshold{Skip: true},
	}}
	tests := []struct {
		name     string
		env      string
		wantSkip bool
		want     string
	}{
		{"mac", acceptance.EnvMac, false, "n=1"},
		{"ci skip", acceptance.EnvCI, true, "-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vals, skip := c.Thresholds(tc.env)
			assert.Equal(t, tc.wantSkip, skip)
			assert.Equal(t, tc.want, formatMap(vals))
		})
	}
}

func TestRenderReport(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{ID: "A-1", Status: StatusPass, Measured: "n=1", Threshold: "n=1"},
		{ID: "A-2", Status: StatusFail, Measured: "n=0", Threshold: "n=1"},
		{ID: "A-3", Status: StatusFail, Measured: "x", Threshold: "y"},
		{ID: "A-4", Status: StatusSkipped, Note: "requires lmstudio — probe: refused"},
		{ID: "A-5", Status: StatusBlocked, Note: "docker info failed"},
	}
	nm := map[string]NotMet{"A-2": {Cause: "slow"}}
	got := renderReport(Header{Project: "triage", Version: "v0.1.0", Date: "2026-10-19", Env: "mac"}, rows, nm)
	for _, want := range []string{
		"# Acceptance report — triage v0.1.0 — 2026-10-19\nEnvironment: mac\n",
		"| ID | Result | Measured | Threshold |\n|---|---|---|---|\n",
		"| A-1 | PASS | n=1 | n=1 |",
		"## Not met\n- A-2: slow\n- A-3: no explanation in not_met.yaml\n",
		"## Skipped (environment)\n- A-4: requires lmstudio — probe: refused\n",
		"## Blocked (environment)\n- A-5: docker info failed\n",
	} {
		assert.Contains(t, got, want)
	}
	summary, failed := summarize(rows)
	assert.True(t, failed)
	assert.True(t, strings.HasPrefix(summary, "FAIL"))
	_, failed = summarize(rows[:1])
	assert.False(t, failed)
}

func TestGoTestArgs(t *testing.T) {
	t.Parallel()
	assert.Contains(t, goTestArgs("TestAcceptance_A"), "-json")
	b := goTestArgs("BenchmarkAcceptance_B")
	assert.Contains(t, b, "^BenchmarkAcceptance_B$")
	assert.Contains(t, b, "1x")
	assert.Contains(t, b, criterionLimit.String(), "go test timeout = runner limit")
}

func TestLMStudioModels(t *testing.T) {
	t.Setenv(envWeakModel, "")
	t.Setenv(envStrongModel, " c-d ")
	assert.Equal(t, []string{defaultWeakModel, "c-d"}, lmstudioModels())
}

func TestParseVMStat(t *testing.T) {
	t.Parallel()
	out := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:  10.\n" +
		"Pages inactive: 20.\nPages speculative: 30.\n"
	n, err := parseVMStat(out)
	require.NoError(t, err)
	assert.EqualValues(t, 60*16384, n)
	_, err = parseVMStat("nothing")
	require.Error(t, err)
}

func TestServicesToStop(t *testing.T) {
	t.Parallel()
	all := []string{"triage", "fakellm"}
	lm := &acceptance.Criterion{Requires: []string{"lmstudio"}, RealDeps: []string{"lmstudio"}, Kind: kindEvaluation}
	perf := &acceptance.Criterion{Kind: kindPerformance, RealDeps: []string{"fakellm"}}
	eval := &acceptance.Criterion{Kind: kindEvaluation, RealDeps: []string{"network"}}
	fn := &acceptance.Criterion{Kind: "functional"}
	assert.Equal(t, all, servicesToStop(lm, all))
	assert.Equal(t, []string{"triage"}, servicesToStop(perf, all))
	assert.Equal(t, []string{"triage"}, servicesToStop(eval, all))
	assert.Empty(t, servicesToStop(fn, all))
}
