// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/app/eval"
	"github.com/thnhdatle/triage/internal/config"
)

type evalConfig struct {
	Mode    string `yaml:"mode"`
	Config  string `yaml:"config"`
	Route   string `yaml:"route"`
	Dataset struct {
		JSONL   string   `yaml:"jsonl"`
		Sets    []string `yaml:"sets"`
		Prompts string   `yaml:"prompts"`
		DataDir string   `yaml:"data_dir"`
		Labels  struct {
			Weak   string `yaml:"weak"`
			Strong string `yaml:"strong"`
		} `yaml:"labels"`
	} `yaml:"dataset"`
	Targets struct {
		Weak   evalTarget `yaml:"weak"`
		Strong evalTarget `yaml:"strong"`
	} `yaml:"targets"`
	Generation struct {
		Temperature *float64 `yaml:"temperature"`
		Seed        *int64   `yaml:"seed"`
		MaxTokens   int      `yaml:"max_tokens"`
	} `yaml:"generation"`
	Strategies    []string `yaml:"strategies"`
	ThresholdRule struct {
		Calibration            string  `yaml:"calibration"`
		MinAccuracyOfAllStrong float64 `yaml:"min_accuracy_of_all_strong"`
	} `yaml:"threshold_rule"`
	Bootstrap struct {
		Resamples int   `yaml:"resamples"`
		Seed      int64 `yaml:"seed"`
	} `yaml:"bootstrap"`
	CacheDir string `yaml:"cache_dir"`
	OutDir   string `yaml:"out_dir"`
}

type evalTarget struct {
	Provider string         `yaml:"provider"`
	Model    string         `yaml:"model"`
	Pricing  config.Pricing `yaml:"pricing"`
}

const (
	evalLive   = "live"
	evalLabels = "labels"
)

const defaultResamples = 10000

const jsonlExt = ".jsonl"

func loadEvalConfig(path string) (*evalConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-chosen eval config
	if err != nil {
		return nil, fmt.Errorf("eval config: %w", err)
	}
	var ec evalConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&ec); err != nil {
		return nil, fmt.Errorf("eval config %s: %w", path, err)
	}
	var problems []string
	if ec.Mode != evalLive && ec.Mode != evalLabels {
		problems = append(problems, fmt.Sprintf("mode: must be live or labels, got %q", ec.Mode))
	}
	if ec.Config == "" || ec.Route == "" {
		problems = append(problems, "config and route are required")
	}
	problems = append(problems, ec.datasetProblems()...)
	problems = append(problems, ec.runProblems()...)
	if len(problems) > 0 {
		return nil, &config.ValidationError{Problems: problems}
	}
	if ec.Bootstrap.Resamples <= 0 {
		ec.Bootstrap.Resamples = defaultResamples
	}
	if ec.ThresholdRule.MinAccuracyOfAllStrong == 0 {
		ec.ThresholdRule.MinAccuracyOfAllStrong = eval.QualityFraction
	}
	if ec.CacheDir == "" {
		ec.CacheDir = "~/.cache/rayld/triage/eval"
	}
	return &ec, nil
}

func isSetFile(path string) bool { return path != "" && !strings.HasSuffix(path, jsonlExt) }

func (ec *evalConfig) runProblems() []string {
	var problems []string
	if m := ec.ThresholdRule.MinAccuracyOfAllStrong; m != 0 && !(m > 0 && m <= 1) {
		problems = append(problems, "threshold_rule.min_accuracy_of_all_strong must be in (0, 1]")
	}
	if ec.Mode == evalLive && (ec.Targets.Weak.Model == "" || ec.Targets.Strong.Model == "") {
		problems = append(problems, "targets.weak and targets.strong are required in live mode")
	}
	if len(ec.Strategies) == 0 {
		problems = append(problems, "strategies: at least one strategy is required")
	}
	return problems
}

func (ec *evalConfig) datasetProblems() []string {
	var problems []string
	if ec.Dataset.JSONL == "" && len(ec.Dataset.Sets) == 0 {
		problems = append(problems, "dataset.jsonl or dataset.sets is required")
	}
	if len(ec.Dataset.Sets) > 0 || isSetFile(ec.ThresholdRule.Calibration) {
		if ec.Mode == evalLive && ec.Dataset.Prompts == "" {
			problems = append(problems, "dataset.prompts is required with dataset.sets in live mode")
		}
		if ec.Mode == evalLabels && (ec.Dataset.Labels.Weak == "" || ec.Dataset.Labels.Strong == "") {
			problems = append(problems, "dataset.labels.weak and dataset.labels.strong are required "+
				"with dataset.sets in labels mode")
		}
	}
	return problems
}

// eval runs the eval subcommand.
func (c *CLI) eval(ctx context.Context, args []string) error {
	fs := c.flags("eval")
	path := fs.String("config", "", "evaluation config (YAML)")
	out := fs.String("out", "", "output directory (default out_dir, else out/eval/<timestamp>)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *path == "" {
		return usagef("eval needs --config eval.yaml")
	}
	ec, err := loadEvalConfig(*path)
	if err != nil {
		return asUsage(err)
	}
	if *out != "" {
		ec.OutDir = *out
	}
	if ec.OutDir == "" {
		ec.OutDir = filepath.Join("out", "eval", time.Now().UTC().Format("20060102T150405Z"))
	}
	r, err := newEvalRun(ctx, c, ec)
	if err != nil {
		return err
	}
	defer r.close(ctx)
	return r.run(ctx)
}

type itemResult struct {
	id                          string
	weakCorrect, strongCorrect  bool
	weakCost, strongCost        float64
	weakTokens, strongTokens    int
	weakOutTokens, strongOutTok int
}

type strategyResult struct {
	name      string
	err       error
	scores    []float64
	threshold float64
	calRatio  float64
	curve     []curvePoint
}

type curvePoint struct {
	threshold, strongRatio, accuracy, cost, randomAcc, pgr float64
	accCI                                                  [2]float64
	chosen                                                 bool
}

type summary struct {
	strongRatio, accuracy, cost, apgr, randomAcc float64
	accCI, costCI, apgrCI                        [2]float64
}

const sweepStep = 0.05

func routedStrong(score, threshold float64) bool {
	switch {
	case score > 1:
		return true
	case score < 0:
		return false
	}
	return score >= threshold
}

func strongShare(scores []float64, threshold float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	n := 0
	for _, s := range scores {
		if routedStrong(s, threshold) {
			n++
		}
	}
	return float64(n) / float64(len(scores))
}

func outcomeAt(res []itemResult, scores []float64, threshold float64) (acc, cost []float64) {
	acc, cost = make([]float64, len(res)), make([]float64, len(res))
	for i, it := range res {
		strong := routedStrong(scores[i], threshold)
		acc[i] = b2f(pick(strong, it.strongCorrect, it.weakCorrect))
		cost[i] = pick(strong, it.strongCost, it.weakCost)
	}
	return acc, cost
}

func sumOver(v []float64, idx []int) float64 {
	var s float64
	for _, i := range idx {
		s += v[i]
	}
	return s
}

func allIndex(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

func tierAccuracy(res []itemResult) (weak, strong float64) {
	for _, it := range res {
		weak += b2f(it.weakCorrect)
		strong += b2f(it.strongCorrect)
	}
	n := float64(len(res))
	return weak / n, strong / n
}

func sweepThresholds(scores []float64, chosen float64) []float64 {
	var out []float64
	add := func(t float64) {
		for _, o := range out {
			if o == t {
				return
			}
		}
		out = append(out, t)
	}
	for k := 0; k <= int(math.Round(1/sweepStep)); k++ {
		add(eval.ThresholdForStrongRatio(scores, float64(k)*sweepStep))
	}
	add(chosen)
	sort.Float64s(out)
	slices.Reverse(out)
	return out
}

func (r *evalRun) sweep(res []itemResult, sr strategyResult) []curvePoint {
	accWeak, accStrong := tierAccuracy(res)
	n := float64(len(res))
	seed, resamples := r.ec.Bootstrap.Seed, r.ec.Bootstrap.Resamples
	pts := make([]curvePoint, 0, 24)
	for _, t := range sweepThresholds(sr.scores, sr.threshold) {
		acc, cost := outcomeAt(res, sr.scores, t)
		p := curvePoint{
			threshold: t, strongRatio: strongShare(sr.scores, t), chosen: t == sr.threshold,
			accuracy: sumOver(acc, allIndex(len(res))) / n, cost: sumOver(cost, allIndex(len(res))),
		}
		p.randomAcc = eval.RandomExpectedAccuracy(p.strongRatio, accWeak, accStrong)
		p.pgr = math.NaN()
		if accStrong != accWeak {
			p.pgr = (p.accuracy - accWeak) / (accStrong - accWeak)
		}
		p.accCI[0], p.accCI[1] = eval.Bootstrap(len(res), resamples, seed, func(idx []int) float64 {
			return sumOver(acc, idx) / float64(len(idx))
		})
		pts = append(pts, p)
	}
	return pts
}

func (r *evalRun) summarise(res []itemResult, sr strategyResult) summary {
	n := len(res)
	items := make([]eval.Item, n)
	for i, it := range res {
		items[i] = eval.Item{Score: sr.scores[i], WeakCorrect: it.weakCorrect, StrongCorrect: it.strongCorrect}
	}
	acc, cost := outcomeAt(res, sr.scores, sr.threshold)
	all := allIndex(n)
	s := summary{
		strongRatio: strongShare(sr.scores, sr.threshold),
		accuracy:    sumOver(acc, all) / float64(n), cost: sumOver(cost, all),
		apgr: apgrOrNaN(items),
	}
	accWeak, accStrong := tierAccuracy(res)
	s.randomAcc = eval.RandomExpectedAccuracy(s.strongRatio, accWeak, accStrong)
	seed, resamples := r.ec.Bootstrap.Seed, r.ec.Bootstrap.Resamples
	s.accCI[0], s.accCI[1] = eval.Bootstrap(n, resamples, seed, func(idx []int) float64 {
		return sumOver(acc, idx) / float64(len(idx))
	})
	s.costCI[0], s.costCI[1] = eval.Bootstrap(n, resamples, seed, func(idx []int) float64 {
		return sumOver(cost, idx) / float64(len(idx)) * float64(n)
	})
	s.apgrCI[0], s.apgrCI[1] = eval.Bootstrap(n, resamples, seed, func(idx []int) float64 {
		sub := make([]eval.Item, len(idx))
		for k, i := range idx {
			sub[k] = items[i]
		}
		return apgrOrNaN(sub)
	})
	return s
}

func apgrOrNaN(items []eval.Item) float64 {
	v, err := eval.APGR(items)
	if err != nil {
		return math.NaN()
	}
	return v
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

var errNotMeasured = errors.New("not measured")
