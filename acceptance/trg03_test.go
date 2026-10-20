// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/wire"
)

const (
	trg03ID             = "TRG-03"
	trg03RunTimeout     = 120 * time.Minute // far above the ~35 min of a cold run
	trg03DefaultBaseURL = "http://localhost:1234/v1"
	trg03ONNXLib        = "/opt/homebrew/lib/libonnxruntime.dylib"
	trg03NotMeasuredMF  = "not measured (requires openai)"
)

// trg03Row is the chosen (calibrated) point of one strategy in curve.csv.
type trg03Row struct {
	Threshold, StrongRatio, Accuracy, Cost, RandomAccuracy float64
}

// trg03Tiers are the per-tier figures read from results.csv.
type trg03Tiers struct {
	Items              int
	AccWeak, AccStrong float64
}

func trg03Num(tb testing.TB, ths map[string]any, key string) float64 {
	tb.Helper()
	switch v := ths[key].(type) {
	case int:
		return float64(v)
	case float64:
		return v
	}
	tb.Fatalf("threshold %s missing or not a number: %v", key, ths[key])
	return 0
}

func trg03ReadCSV(tb testing.TB, path string) []map[string]string {
	tb.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: path under the test's own output dir
	require.NoError(tb, err)
	defer func() { require.NoError(tb, f.Close()) }()
	recs, err := csv.NewReader(f).ReadAll()
	require.NoError(tb, err)
	require.NotEmpty(tb, recs, path)
	rows := make([]map[string]string, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		m := make(map[string]string, len(rec))
		for i, h := range recs[0] {
			m[h] = rec[i]
		}
		rows = append(rows, m)
	}
	return rows
}

func trg03Float(tb testing.TB, s string) float64 {
	tb.Helper()
	v, err := strconv.ParseFloat(s, 64)
	require.NoError(tb, err, s)
	return v
}

// trg03YAML reads a YAML file into a generic map so single keys can be overridden.
func trg03YAML(tb testing.TB, path string) map[string]any {
	tb.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: repository file
	require.NoError(tb, err)
	var m map[string]any
	require.NoError(tb, yaml.Unmarshal(data, &m))
	return m
}

func trg03Sub(tb testing.TB, m map[string]any, key string) map[string]any {
	tb.Helper()
	sub, ok := m[key].(map[string]any)
	require.True(tb, ok, "%s is not a mapping", key)
	return sub
}

func trg03WriteYAML(tb testing.TB, path string, m map[string]any) {
	tb.Helper()
	data, err := yaml.Marshal(m)
	require.NoError(tb, err)
	require.NoError(tb, os.WriteFile(path, data, 0o600))
}

func trg03Env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// trg03SetCount counts the item ids of a set file (blank and # lines excluded).
func trg03SetCount(tb testing.TB, path string) int {
	tb.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: repository file
	require.NoError(tb, err)
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			n++
		}
	}
	return n
}

// trg03Prepare writes the run's config and eval config into dir and returns the eval config path.
// Paths become absolute (the test runs in acceptance/); the bert variant is pinned to int8 with a
// generous scoring timeout so a slow inference can never fall back to the strong tier silently.
func trg03Prepare(tb testing.TB, root, dir, out string, strategies []string) string {
	tb.Helper()
	ec := trg03YAML(tb, filepath.Join(root, "eval", "trg03.yaml"))

	cfg := trg03YAML(tb, filepath.Join(root, fmt.Sprint(ec["config"])))
	lm := trg03Sub(tb, trg03Sub(tb, cfg, "providers"), "lmstudio")
	lm["base_url"] = trg03Env("TRIAGE_LMSTUDIO_BASE_URL", trg03DefaultBaseURL)
	bertCfg := trg03Sub(tb, trg03Sub(tb, cfg, "difficulty"), "bert")
	bertCfg["variant"] = "int8"
	bertCfg["timeout"] = "10s"
	cfgPath := filepath.Join(dir, "config.yaml")
	trg03WriteYAML(tb, cfgPath, cfg)

	ec["config"] = cfgPath
	ec["out_dir"] = out
	ec["strategies"] = strategies
	ds := trg03Sub(tb, ec, "dataset")
	orig, _ := ds["sets"].([]any)
	sets := make([]any, 0, len(orig))
	for _, s := range orig {
		sets = append(sets, filepath.Join(root, fmt.Sprint(s)))
	}
	ds["sets"] = sets
	ds["prompts"] = filepath.Join(root, fmt.Sprint(ds["prompts"]))
	rule := trg03Sub(tb, ec, "threshold_rule")
	rule["calibration"] = filepath.Join(root, fmt.Sprint(rule["calibration"]))
	targets := trg03Sub(tb, ec, "targets")
	trg03Sub(tb, targets, "weak")["model"] = trg03Env("TRIAGE_TRG03_WEAK_MODEL", "qwen/qwen3-vl-4b")
	trg03Sub(tb, targets, "strong")["model"] = trg03Env("TRIAGE_TRG03_STRONG_MODEL", "gemma-4-26b-a4b-it-qat-mlx")
	path := filepath.Join(dir, "eval.yaml")
	trg03WriteYAML(tb, path, ec)
	return path
}

// trg03Chosen returns the chosen curve point of a strategy and the largest cost over its sweep
// (the all-strong cost: the sweep reaches strong ratio 1).
func trg03Chosen(
	tb testing.TB, curve []map[string]string, strategy string,
) (row trg03Row, allStrongCost float64, ok bool) {
	tb.Helper()
	for _, r := range curve {
		if r["strategy"] != strategy {
			continue
		}
		cost := trg03Float(tb, r["cost_usd"])
		if trg03Float(tb, r["strong_ratio"]) >= 1-1e-9 {
			allStrongCost = math.Max(allStrongCost, cost)
		}
		if r["chosen"] == "true" {
			row = trg03Row{
				Threshold: trg03Float(tb, r["threshold"]), StrongRatio: trg03Float(tb, r["strong_ratio"]),
				Accuracy: trg03Float(tb, r["accuracy"]), Cost: cost, RandomAccuracy: trg03Float(tb, r["random_accuracy"]),
			}
			ok = true
		}
	}
	return row, allStrongCost, ok
}

// trg03TierAccuracy reads the all-weak and all-strong accuracy of a strategy's eval rows.
func trg03TierAccuracy(tb testing.TB, results []map[string]string, strategy string) trg03Tiers {
	tb.Helper()
	var t trg03Tiers
	var weak, strong float64
	for _, r := range results {
		if r["strategy"] != strategy {
			continue
		}
		t.Items++
		if r["weak_correct"] == "true" {
			weak++
		}
		if r["strong_correct"] == "true" {
			strong++
		}
	}
	require.Positive(tb, t.Items, "no eval rows for %s", strategy)
	t.AccWeak, t.AccStrong = weak/float64(t.Items), strong/float64(t.Items)
	return t
}

// TestAcceptance_TRG03_CostQuality implements TRG-03: `triage eval` live mode over the 100 eval
// items (calibration on 100 disjoint items) with the weak/strong LM Studio pair, then the gates on
// the bert strategy at its calibrated threshold.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG03_CostQuality(t *testing.T) {
	th := thresholds(t, trg03ID)
	wantEval := int(trg03Num(t, th, "eval_items"))
	wantCal := int(trg03Num(t, th, "calibration_items"))
	costMax := trg03Num(t, th, "cost_ratio_max")
	accRatioMin := trg03Num(t, th, "accuracy_ratio_min")
	randomDelta := trg03Num(t, th, "accuracy_vs_random_min_delta")
	costTarget := trg03Num(t, th, "cost_ratio_target_report_only")
	minAccCal := trg03Num(t, th, "min_accuracy_of_all_strong_calibration")

	root, err := filepath.Abs("..")
	require.NoError(t, err)
	if os.Getenv("TRIAGE_ONNX_LIBRARY_PATH") == "" {
		if _, err := os.Stat(trg03ONNXLib); err == nil {
			t.Setenv("TRIAGE_ONNX_LIBRARY_PATH", trg03ONNXLib)
		}
	}
	// LM Studio holds one model at a time: free its memory when the run is over.
	t.Cleanup(func() {
		out, err := exec.CommandContext(context.WithoutCancel(t.Context()), "lms", "unload", "--all").CombinedOutput()
		t.Logf("lms unload --all: err=%v output=%s", err, strings.TrimSpace(string(out)))
	})

	strategies := []string{"random", "length", "bert"}
	haveMF := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
	if haveMF {
		strategies = append(strategies, "mf")
	}
	runName := "trg03-" + time.Now().UTC().Format("20060102T150405Z")
	out := filepath.Join(root, "out", "eval", runName)
	dir := t.TempDir()
	evalPath := trg03Prepare(t, root, dir, out, strategies)

	// The eval answers weak first, then strong, one batch each (evalrun.go outcomes), and caches
	// them under ~/.cache/rayld/triage/eval/answers, so a re-run costs no completions.
	ctx, cancel := context.WithTimeout(t.Context(), trg03RunTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cli := &wire.CLI{Stdout: &stdout, Stderr: &stderr}
	start := time.Now()
	code := cli.Run(ctx, []string{"eval", "--config", evalPath})
	t.Logf("triage eval finished in %s, exit %d, results in %s", time.Since(start).Round(time.Second), code, out)
	require.Zero(t, code, "triage eval failed: %s", stderr.String())
	t.Logf("summary.md:\n%s", stdout.String())

	curve := trg03ReadCSV(t, filepath.Join(out, "curve.csv"))
	results := trg03ReadCSV(t, filepath.Join(out, "results.csv"))

	tiers := trg03TierAccuracy(t, results, "bert")
	bertRow, allStrongCost, ok := trg03Chosen(t, curve, "bert")
	require.True(t, ok, "bert has no chosen point in curve.csv (see summary.md in %s)", out)
	require.Positive(t, allStrongCost, "all-strong cost is zero: is the strong target priced?")

	costRatio := bertRow.Cost / allStrongCost
	accRatio := bertRow.Accuracy / tiers.AccStrong
	r := bertRow.StrongRatio
	randomExpected := (1-r)*tiers.AccWeak + r*tiers.AccStrong
	calCount := trg03SetCount(t, filepath.Join(root, "eval", "sets", "trg03_cal.txt"))

	measured := map[string]any{
		"eval_items": tiers.Items, "calibration_items": calCount, "run_dir": out,
		"min_accuracy_of_all_strong_calibration": minAccCal,
		"bert_threshold":                         bertRow.Threshold,
		"bert_strong_call_ratio":                 r,
		"bert_accuracy":                          bertRow.Accuracy,
		"all_weak_accuracy":                      tiers.AccWeak,
		"all_strong_accuracy":                    tiers.AccStrong,
		"bert_cost_usd":                          bertRow.Cost,
		"all_strong_cost_usd":                    allStrongCost,
		"cost_ratio":                             costRatio,
		"cost_ratio_target_report_only":          costTarget,
		"cost_ratio_target_met":                  costRatio <= costTarget,
		"accuracy_ratio":                         accRatio,
		"random_expected_accuracy_same_ratio":    randomExpected,
		"accuracy_minus_random":                  bertRow.Accuracy - randomExpected,
	}
	for _, s := range []string{"random", "length", "mf"} {
		if s == "mf" && !haveMF {
			measured["mf"] = trg03NotMeasuredMF
			continue
		}
		row, _, found := trg03Chosen(t, curve, s)
		if !found {
			measured[s] = "not measured (see summary.md)"
			continue
		}
		measured[s] = map[string]any{
			"threshold": row.Threshold, "strong_call_ratio": row.StrongRatio, "accuracy": row.Accuracy,
			"cost_ratio": row.Cost / allStrongCost,
		}
	}

	var problems []string
	if tiers.Items != wantEval {
		problems = append(problems, fmt.Sprintf("eval items %d, want %d", tiers.Items, wantEval))
	}
	if calCount != wantCal {
		problems = append(problems, fmt.Sprintf("calibration items %d, want %d", calCount, wantCal))
	}
	if costRatio > costMax {
		problems = append(problems, fmt.Sprintf("cost ratio %.3f > %.2f", costRatio, costMax))
	}
	if accRatio < accRatioMin {
		problems = append(problems, fmt.Sprintf("accuracy ratio %.3f < %.2f", accRatio, accRatioMin))
	}
	if bertRow.Accuracy < randomExpected+randomDelta {
		problems = append(problems, fmt.Sprintf("accuracy %.3f < random expected %.3f + %.2f at strong ratio %.2f",
			bertRow.Accuracy, randomExpected, randomDelta, r))
	}
	pass := len(problems) == 0
	t.Logf("TRG-03 bert: cost ratio %.3f (target %.2f, report only), accuracy ratio %.3f, accuracy %.3f vs random %.3f, "+
		"strong ratio %.2f, threshold %.4f; random/length/mf: %v / %v / %v",
		costRatio, costTarget, accRatio, bertRow.Accuracy, randomExpected, r, bertRow.Threshold,
		measured["random"], measured["length"], measured["mf"])
	writeResult(t, Result{ID: trg03ID, Measured: measured, Pass: pass, Details: strings.Join(problems, "; ")})
	require.True(t, pass, "measured %v; problems: %v", measured, problems)
}
