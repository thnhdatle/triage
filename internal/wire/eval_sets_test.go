// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/wire"
)

const (
	fixtureRows   = 12
	labelsWeakCol = "weak-model"
	labelsStrCol  = "strong-model"
)

func evalDataFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	var gsm, labels, ml strings.Builder
	lw, mw := csv.NewWriter(&labels), csv.NewWriter(&ml)
	require.NoError(t, lw.Write([]string{"prompt", labelsWeakCol, labelsStrCol}))
	require.NoError(t, mw.Write([]string{"prompt", labelsWeakCol, labelsStrCol}))
	for i := range fixtureRows {
		q := fmt.Sprintf("Problem %d: %s", i, strings.Repeat("word ", 300*i))
		b, err := json.Marshal(map[string]string{"question": q, "answer": fmt.Sprintf("work #### %d", i)})
		require.NoError(t, err)
		gsm.Write(b)
		gsm.WriteByte('\n')
		require.NoError(t, lw.Write([]string{q, strconv.FormatBool(i < 4), "True"}))
	}
	lw.Flush()
	mk("gsm8k/test.jsonl", gsm.String())
	mk("routellm/gsm8k/gsm8k_responses.csv", labels.String())
	first, err := json.Marshal(map[string]string{"eval_prompt": "Problem 0: "})
	require.NoError(t, err)
	mk("routellm/gsm8k/contaminated_prompts.jsonl", string(first)+"\n")

	var mm strings.Builder
	for i := range 6 {
		row := map[string]any{
			"subject": "anatomy", "question": fmt.Sprintf("Which bone %d? %s", i, strings.Repeat("x ", 200*i)),
			"choices": []string{"femur", "tibia", "ulna", "radius"}, "answer": i % 4,
		}
		b, err := json.Marshal(row)
		require.NoError(t, err)
		mm.Write(b)
		mm.WriteByte('\n')
		prompt := fmt.Sprintf("%s\nA. femur\nB. tibia\nC. ulna\nD. radius\nAnswer:", row["question"])
		require.NoError(t, mw.Write([]string{prompt, strconv.FormatBool(i < 3), "True"}))
	}
	mw.Flush()
	mk("mmlu/test/anatomy.jsonl", mm.String())
	mk("routellm/mmlu/responses/mmlu_anatomy.csv", ml.String())
	return dir
}

func writeSet(t *testing.T, name string, ids ...string) string {
	t.Helper()
	return writeFile(t, name, "# set\n"+strings.Join(ids, "\n")+"\n")
}

func thresholdOf(t *testing.T, summary, strategy string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\| ` + strategy + ` \| ([0-9.]+) \|`).FindStringSubmatch(summary)
	require.NotNil(t, m, summary)
	return m[1]
}

func gsmIDs(from, to int) []string {
	var ids []string
	for i := from; i < to; i++ {
		ids = append(ids, fmt.Sprintf("gsm8k/test:%d", i))
	}
	return ids
}

func TestCLI_EvalSets_LabelsSweepAndThresholdRule(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FAKE_KEY": "k"}
	cfg := writeFile(t, "c.yaml", offlineConfig(lengthRoute))
	data := evalDataFixture(t)
	cal := writeSet(t, "cal.txt", append(gsmIDs(1, 7), "mmlu/test:anatomy:0", "mmlu/test:anatomy:5")...)
	evalSet := writeSet(t, "eval.txt", append(gsmIDs(1, fixtureRows), "mmlu/test:anatomy:1")...)
	run := func(minAcc string) (summary, csv, curve string) {
		out := filepath.Join(t.TempDir(), "run")
		ev := writeFile(t, "eval.yaml", `
mode: labels
config: `+cfg+`
route: auto
dataset:
  sets: [`+cal+`, `+evalSet+`]
  data_dir: `+data+`
  labels: { weak: `+labelsWeakCol+`, strong: `+labelsStrCol+` }
strategies: [random, length]
threshold_rule: { calibration: `+cal+minAcc+` }
bootstrap: { resamples: 100, seed: 7 }
`)
		r := runCLI(t, env, "eval", "--config", ev, "--out", out)
		require.Equal(t, wire.ExitOK, r.code, r.stderr)
		read := func(n string) string {
			b, err := os.ReadFile(filepath.Join(out, n))
			require.NoError(t, err)
			return string(b)
		}
		return read("summary.md"), read("results.csv"), read("curve.csv")
	}

	summary, csv, curve := run("")
	items := len(gsmIDs(7, fixtureRows)) + 1
	assert.Len(t, strings.Split(strings.TrimSpace(csv), "\n"), 1+2*items, "calibration items are not evaluated")
	assert.Contains(t, summary, "Threshold: lowest strong-call ratio on "+filepath.Base(cal)+" (")
	assert.NotContains(t, summary, filepath.Dir(cal), "no host-specific directory in the summary")
	assert.Contains(t, summary, ">= 96% of all-strong", "default fraction")
	assert.Contains(t, summary, "## Sweep: length")
	assert.Contains(t, summary, "(chosen)")
	assert.Contains(t, summary, "Reference: all-weak accuracy")
	rows := strings.Split(strings.TrimSpace(curve), "\n")
	assert.Equal(t, "strategy,threshold,strong_ratio,accuracy,accuracy_ci_lo,accuracy_ci_hi,"+
		"cost_usd,random_accuracy,pgr,chosen", rows[0])
	assert.GreaterOrEqual(t, len(rows), 1+2*5, "several sweep points per strategy (equal thresholds merge on few items)")
	assert.Equal(t, 2, strings.Count(curve, ",true\n"), "one chosen point per strategy")

	strict, _, _ := run(", min_accuracy_of_all_strong: 1.0")
	loose, _, _ := run(", min_accuracy_of_all_strong: 0.5")
	assert.Contains(t, strict, ">= 100% of all-strong")
	assert.Contains(t, loose, ">= 50% of all-strong")
	assert.NotEqual(t, thresholdOf(t, strict, "length"), thresholdOf(t, loose, "length"),
		"the threshold follows min_accuracy_of_all_strong")

	bad := writeFile(t, "bad.yaml", "mode: labels\nconfig: "+cfg+"\nroute: auto\ndataset: { jsonl: x.jsonl }\n"+
		"strategies: [length]\nthreshold_rule: { min_accuracy_of_all_strong: 1.5 }\n")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval", "--config", bad).code)
}

func TestCLI_EvalSets_WildcardAndErrors(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FAKE_KEY": "k"}
	cfg := writeFile(t, "c.yaml", offlineConfig(lengthRoute))
	data := evalDataFixture(t)
	all := writeSet(t, "all.txt", "gsm8k/test:*")
	conf := func(sets, extra string) string {
		return writeFile(t, "eval.yaml", `
mode: labels
config: `+cfg+`
route: auto
dataset:
  sets: [`+sets+`]
  data_dir: `+data+`
  labels: { weak: `+labelsWeakCol+`, strong: `+labelsStrCol+` }
strategies: [length]
bootstrap: { resamples: 50, seed: 7 }
`+extra)
	}
	out := filepath.Join(t.TempDir(), "run")
	r := runCLI(t, env, "eval", "--config", conf(all, ""), "--out", out)
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Contains(t, r.stdout, fmt.Sprintf("%d items", fixtureRows-1), "the contaminated row is excluded")

	missing := writeSet(t, "missing.txt", "gsm8k/test:999")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval", "--config", conf(missing, ""), "--out", out).code)
	badID := writeSet(t, "bad.txt", "gsm8k/train:1")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval", "--config", conf(badID, ""), "--out", out).code)
	nolabels := writeFile(t, "nl.yaml", "mode: labels\nconfig: "+cfg+"\nroute: auto\ndataset: { sets: ["+all+"] }\n"+
		"strategies: [length]\n")
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval", "--config", nolabels, "--out", out).code)
	assert.Equal(t, wire.ExitUsage, runCLI(t, env, "eval", "--config",
		conf(all, "threshold_rule: { calibration: "+all+" }\n"), "--out", out).code)
}

func TestCLI_EvalSets_LiveOneBatchPerModel(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	cfg := writeFile(t, "c.yaml", baseConfig(f, defaultRoutes))
	data := evalDataFixture(t)
	prompts := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(prompts, "gsm8k.txt"),
		[]byte("Solve: {{.Question}}\nThe answer is N.\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(prompts, "mmlu.txt"),
		[]byte("About {{.Subject}}.\n{{.Question}}\n{{range .Choices}}{{.Letter}}) {{.Text}}\n{{end}}Letter only.\n"), 0o600))
	cal := writeSet(t, "cal.txt", "gsm8k/test:1", "gsm8k/test:2", "mmlu/test:anatomy:0")
	evalSet := writeSet(t, "eval.txt", "gsm8k/test:3", "gsm8k/test:4", "mmlu/test:anatomy:1")
	cache := t.TempDir()
	ev := writeFile(t, "eval.yaml", `
mode: live
config: `+cfg+`
route: triage-auto
dataset: { sets: [`+cal+`, `+evalSet+`], prompts: `+prompts+`, data_dir: `+data+` }
targets:
  weak: { provider: fake, model: fake-small }
  strong: { provider: fake, model: fake-strong, pricing: { input_per_mtok: 1.25, output_per_mtok: 10 } }
generation: { temperature: 0, seed: 7, max_tokens: 64 }
strategies: [random, length]
threshold_rule: { calibration: `+cal+`, min_accuracy_of_all_strong: 0.9 }
bootstrap: { resamples: 50, seed: 7 }
cache_dir: `+cache+`
`)
	f.script(t, `[{"model":"fake-strong","scenario":"echo","count":6}]`)
	out := filepath.Join(t.TempDir(), "a")
	r := runCLI(t, nil, "eval", "--config", ev, "--out", out)
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	reqs := f.requests(t)
	require.Len(t, reqs, 12, "(3 calibration + 3 evaluation items) x 2 models")
	for i, rq := range reqs {
		want := "fake-small"
		if i >= 6 {
			want = "fake-strong"
		}
		assert.Equal(t, want, rq.Model, "one batch per model, never interleaved (request %d)", i)
	}
	assert.Contains(t, reqs[0].Body, "Solve: Problem 3:", "evaluation items first, then calibration items")
	assert.Contains(t, reqs[0].Body, "The answer is N.")
	assert.Contains(t, reqs[2].Body, "About anatomy.")
	assert.Contains(t, reqs[2].Body, "A) femur")
	assert.FileExists(t, filepath.Join(out, "curve.csv"))

	r = runCLI(t, nil, "eval", "--config", ev, "--out", filepath.Join(t.TempDir(), "b"))
	require.Equal(t, wire.ExitOK, r.code, r.stderr)
	assert.Len(t, f.requests(t), 12, "no answer is recomputed")

	noPrompts := writeFile(t, "np.yaml", strings.Replace(mustRead(t, ev), "prompts: "+prompts+", ", "", 1))
	assert.Equal(t, wire.ExitUsage, runCLI(t, nil, "eval", "--config", noPrompts, "--out", out).code)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test file
	require.NoError(t, err)
	return string(b)
}
