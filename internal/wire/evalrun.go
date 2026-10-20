// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/eval"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// evalContextWindow is the window of the synthetic eval targets: selection
// must never drop an eval item for its length.
const evalContextWindow = 1 << 30

type evalRun struct {
	cli      *CLI
	ec       *evalConfig
	cfg      *config.Config
	off      *offline
	cacheDir string
	data     *evalData
	items    []datasetItem
	cal      []datasetItem
}

func newEvalRun(ctx context.Context, c *CLI, ec *evalConfig) (*evalRun, error) {
	cfg, err := config.Load(ec.Config)
	if err != nil {
		return nil, asUsage(err)
	}
	if !hasRoute(cfg, ec.Route) {
		return nil, usagef("eval: route %q is not in %s", ec.Route, ec.Config)
	}
	r := &evalRun{cli: c, ec: ec, cfg: cfg}
	if r.cacheDir, err = c.Env.expandHome(ec.CacheDir); err != nil {
		return nil, err
	}
	if err := r.loadItems(); err != nil {
		return nil, asUsage(err)
	}
	if r.off, err = newOffline(ctx, c.Env, c.logger(), cfg); err != nil {
		return nil, err
	}
	return r, nil
}

// loadItems reads the eval items (JSONL and/or id sets) and the calibration
// items. Items of the calibration set are not evaluated, so the two stay
// disjoint when the calibration set is also listed in dataset.sets.
func (r *evalRun) loadItems() error {
	ds := r.ec.Dataset
	dataDir := ds.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(r.cacheDir, "data")
	}
	dataDir, err := r.cli.Env.expandHome(dataDir)
	if err != nil {
		return err
	}
	r.data = newEvalData(dataDir, ds.Prompts, ds.Labels.Weak, ds.Labels.Strong, r.ec.Mode == evalLive)
	if ds.JSONL != "" {
		if r.items, err = readDataset(ds.JSONL); err != nil {
			return err
		}
	}
	var fromSets []datasetItem
	for _, set := range ds.Sets {
		its, err := r.readSet(set)
		if err != nil {
			return err
		}
		fromSets = append(fromSets, its...)
	}
	if cal := r.ec.ThresholdRule.Calibration; cal != "" {
		if r.cal, err = r.readSet(cal); err != nil {
			return err
		}
	}
	inCal := make(map[string]bool, len(r.cal))
	for _, it := range r.cal {
		inCal[it.ID] = true
	}
	seen := map[string]bool{}
	for _, it := range r.items {
		seen[it.ID] = true
	}
	for _, it := range fromSets {
		if !inCal[it.ID] && !seen[it.ID] {
			seen[it.ID] = true
			r.items = append(r.items, it)
		}
	}
	if len(r.items) == 0 {
		return errors.New("eval: no items left to evaluate (dataset.sets minus the calibration set)")
	}
	return nil
}

// readSet reads a JSONL dataset or an item-id set file.
func (r *evalRun) readSet(path string) ([]datasetItem, error) {
	if !isSetFile(path) {
		return readDataset(path)
	}
	ids, err := readItemIDs(path)
	if err != nil {
		return nil, err
	}
	return r.data.items(ids)
}

func hasRoute(cfg *config.Config, name string) bool {
	for _, r := range cfg.Routes {
		if r.Name == name {
			return true
		}
	}
	return false
}

func (r *evalRun) close(ctx context.Context) { r.off.close(ctx) }

func (r *evalRun) run(ctx context.Context) error {
	// One batch per model over evaluation and calibration items together.
	all := make([]datasetItem, 0, len(r.items)+len(r.cal))
	all = append(append(all, r.items...), r.cal...)
	out, err := r.outcomes(ctx, all)
	if err != nil {
		return err
	}
	res, cal := out[:len(r.items)], out[len(r.items):]
	results := make([]strategyResult, 0, len(r.ec.Strategies))
	for _, s := range r.ec.Strategies {
		sr := r.strategy(ctx, s, cal)
		if sr.err == nil {
			sr.curve = r.sweep(res, sr)
		}
		results = append(results, sr)
	}
	if err := os.MkdirAll(r.ec.OutDir, 0o750); err != nil {
		return fmt.Errorf("eval output: %w", err)
	}
	if err := r.writeCSV(res, results); err != nil {
		return err
	}
	if err := r.writeCurve(results); err != nil {
		return err
	}
	return r.writeSummary(res, results)
}

// outcomes returns each item's correctness and cost under both tiers: from
// labels, or from answers generated per tier in one batch each.
func (r *evalRun) outcomes(ctx context.Context, items []datasetItem) ([]itemResult, error) {
	out := make([]itemResult, len(items))
	for i, it := range items {
		out[i].id = it.ID
	}
	if r.ec.Mode == evalLabels {
		for i, it := range items {
			if it.WeakCorrect == nil || it.StrongCorrect == nil {
				return nil, usagef("eval labels: item %s lacks weak_correct/strong_correct", it.ID)
			}
			out[i].weakCorrect, out[i].strongCorrect = *it.WeakCorrect, *it.StrongCorrect
		}
		return out, nil
	}
	for _, tier := range []model.Tier{model.TierWeak, model.TierStrong} {
		if err := r.generate(ctx, tier, items, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type cachedAnswer struct {
	Answer string     `json:"answer"`
	Usage  chat.Usage `json:"usage"`
}

// generate answers every item with one tier's target through dispatch
// pinned to that target, caching answers by (model, item, prompt hash).
func (r *evalRun) generate(ctx context.Context, tier model.Tier, items []datasetItem, out []itemResult) error {
	t := r.ec.Targets.Weak
	if tier == model.TierStrong {
		t = r.ec.Targets.Strong
	}
	disp, target, err := r.pinned(tier, t)
	if err != nil {
		return err
	}
	for i, it := range items {
		ans, err := r.answer(ctx, disp, target, it)
		if err != nil {
			return fmt.Errorf("eval %s item %s: %w", tier, it.ID, err)
		}
		sc, err := eval.ScorerByName(pick(it.Scorer == "", eval.ScorerExact, it.Scorer))
		if err != nil {
			return usagef("eval item %s: %w", it.ID, err)
		}
		ok, cost := sc(ans.Answer, it.Reference), target.Pricing.Cost(ans.Usage)
		if tier == model.TierStrong {
			out[i].strongCorrect, out[i].strongCost = ok, cost
			out[i].strongTokens, out[i].strongOutTok = ans.Usage.PromptTokens, ans.Usage.CompletionTokens
		} else {
			out[i].weakCorrect, out[i].weakCost = ok, cost
			out[i].weakTokens, out[i].weakOutTokens = ans.Usage.PromptTokens, ans.Usage.CompletionTokens
		}
	}
	return nil
}

func (r *evalRun) pinned(tier model.Tier, t evalTarget) (*dispatch.Dispatcher, model.Target, error) {
	pc, ok := r.cfg.Providers[t.Provider]
	if !ok {
		return nil, model.Target{}, usagef("eval targets.%s: unknown provider %q", tier, t.Provider)
	}
	key, err := r.cli.Env.apiKey(pc.APIKeyEnv, pc.APIKeyFile)
	if err != nil {
		return nil, model.Target{}, fmt.Errorf("provider %s: %w", t.Provider, err)
	}
	d := r.cfg.Dispatch
	up, err := newUpstream(clientSpec{
		Provider: t.Provider, Type: pc.Type, BaseURL: pc.BaseURL, MaxIdle: pc.MaxIdleConnsPerHost,
		Timeout: time.Duration(d.AttemptTimeout), key: key,
	})
	if err != nil {
		return nil, model.Target{}, err
	}
	disp, err := dispatch.New(map[string]provider.Provider{t.Provider: up}, r.off.b.clk, dispatch.Options{
		MaxAttempts: d.MaxAttempts, AttemptTimeout: time.Duration(d.AttemptTimeout),
		TotalTimeout: time.Duration(d.TotalTimeout),
	})
	if err != nil {
		return nil, model.Target{}, fmt.Errorf("eval dispatch: %w", err)
	}
	return disp, evalTargetOf(tier, t), nil
}

func evalTargetOf(tier model.Tier, t evalTarget) model.Target {
	loc := pick(tier == model.TierStrong, model.LocalityCloud, model.LocalityLocal)
	return model.Target{
		Name: "eval-" + string(tier), Provider: t.Provider, Model: t.Model, Locality: loc, Tier: tier,
		ContextWindow: evalContextWindow,
		Pricing: model.Pricing{
			InputPerMTok: t.Pricing.InputPerMTok, OutputPerMTok: t.Pricing.OutputPerMTok,
			CachedInputPerMTok: t.Pricing.CachedInputPerMTok,
		},
	}
}

func (r *evalRun) answer(
	ctx context.Context, disp *dispatch.Dispatcher, target model.Target, it datasetItem,
) (cachedAnswer, error) {
	req, err := it.request(target.Model)
	if err != nil {
		return cachedAnswer{}, err
	}
	g := r.ec.Generation
	req.Temperature, req.Seed = g.Temperature, g.Seed
	if g.MaxTokens > 0 {
		req.MaxTokens = &g.MaxTokens
	}
	key, err := json.Marshal(struct {
		Model, ID, Prompt string
		Messages          []wireMessage
		Temperature       *float64
		Seed              *int64
		MaxTokens         int
	}{target.Model, it.ID, it.Prompt, it.Messages, g.Temperature, g.Seed, g.MaxTokens})
	if err != nil {
		return cachedAnswer{}, fmt.Errorf("cache key: %w", err)
	}
	sum := sha256.Sum256(key)
	path := filepath.Join(r.cacheDir, "answers", hex.EncodeToString(sum[:])+".json")
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // cache path built from a hash
		var a cachedAnswer
		if json.Unmarshal(data, &a) == nil {
			return a, nil
		}
	}
	res, err := disp.Complete(ctx, dispatch.Plan{
		Request: req, Chain: []model.Target{target},
		Localities: model.AllLocalities(),
	})
	if err != nil {
		return cachedAnswer{}, fmt.Errorf("generate: %w", err)
	}
	a := cachedAnswer{Usage: res.Usage}
	if len(res.Response.Choices) > 0 {
		a.Answer = res.Response.Choices[0].Message.Text()
	}
	if err := writeJSONFile(path, a); err != nil {
		return cachedAnswer{}, err
	}
	return a, nil
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	return nil
}

// strategy scores the items through the real route pipeline of the eval
// route with the strategy swapped in; dispatch is a stub, answers come from
// pass 1. Items a rule or privacy pinned to a tier get a score outside [0,1].
func (r *evalRun) strategy(ctx context.Context, name string, cal []itemResult) strategyResult {
	sr := strategyResult{name: name}
	pl, cleanup, err := r.pipeline(ctx, name)
	if err != nil {
		sr.err = fmt.Errorf("%w: %w", errNotMeasured, err)
		return sr
	}
	defer cleanup()
	if sr.scores, err = routeScores(ctx, pl, r.ec.Route, r.items); err != nil {
		sr.err = err
		return sr
	}
	sr.threshold = r.routeThreshold()
	if len(cal) > 0 {
		calScores, err := routeScores(ctx, pl, r.ec.Route, r.cal)
		if err != nil {
			sr.err = err
			return sr
		}
		items := make([]eval.Item, len(cal))
		for i, c := range cal {
			items[i] = eval.Item{Score: calScores[i], WeakCorrect: c.weakCorrect, StrongCorrect: c.strongCorrect}
		}
		minAcc := r.ec.ThresholdRule.MinAccuracyOfAllStrong
		if sr.threshold, sr.calRatio, err = eval.CalibrationThresholdAt(items, minAcc); err != nil {
			sr.err = fmt.Errorf("calibration: %w", err)
		}
	}
	return sr
}

func (r *evalRun) routeThreshold() float64 {
	for _, rt := range r.cfg.Routes {
		if rt.Name == r.ec.Route {
			return rt.Threshold
		}
	}
	return 0
}

// pipeline builds the eval route with strategy name over the two eval
// targets and a stub dispatcher.
func (r *evalRun) pipeline(ctx context.Context, name string) (*route.Pipeline, func(), error) {
	cfg := *r.cfg
	weak, strong := r.ec.Targets.Weak, r.ec.Targets.Strong
	anyProvider := r.defaultProvider()
	weak.Provider = pick(weak.Provider == "", anyProvider, weak.Provider)
	strong.Provider = pick(strong.Provider == "", anyProvider, strong.Provider)
	// Only the providers the eval uses are built: the others (and their
	// secrets) are irrelevant to pass 2.
	keep := map[string]bool{weak.Provider: true, strong.Provider: true}
	if name == strategyMF {
		keep[cfg.Difficulty.MF.Provider] = true
	}
	cfg.Providers = make(map[string]config.Provider, len(keep))
	for n, p := range r.cfg.Providers {
		if keep[n] {
			cfg.Providers[n] = p
		}
	}
	cfg.Targets = []config.Target{evalConfigTarget(model.TierWeak, weak), evalConfigTarget(model.TierStrong, strong)}
	for _, rt := range r.cfg.Routes {
		if rt.Name == r.ec.Route {
			rt.Pool, rt.Fallback, rt.Strategy = []string{"eval-weak", "eval-strong"}, nil, name
			cfg.Routes = []config.Route{rt}
		}
	}
	cfg.Difficulty.MissingArtifact = "error"
	g, err := r.off.b.build(ctx, &cfg)
	if err != nil {
		return nil, nil, err
	}
	routes, err := routesOf(&cfg, g.plan)
	if err != nil {
		r.off.b.retire(ctx, g)
		return nil, nil, err
	}
	pl, err := route.New(route.Deps{
		Targets: g.targets, Routes: routes, Strategies: g.strategies, Detectors: g.detectors,
		Dispatcher: stubDispatcher{}, Recorder: decisionSink{}, Clock: r.off.b.clk, NewID: NewID,
	}, routeOptions(&cfg, g.plan, g.counter))
	if err != nil {
		r.off.b.retire(ctx, g)
		return nil, nil, fmt.Errorf("eval pipeline: %w", err)
	}
	return pl, func() { r.off.b.retire(ctx, g) }, nil
}

// defaultProvider is the provider of eval targets that name none (labels
// mode): the first, by name, that needs no API key, else the first.
func (r *evalRun) defaultProvider() string {
	names := make([]string, 0, len(r.cfg.Providers))
	for n := range r.cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if p := r.cfg.Providers[n]; p.APIKeyEnv == "" && p.APIKeyFile == "" {
			return n
		}
	}
	return pick(len(names) > 0, names[0], "")
}

func evalConfigTarget(tier model.Tier, t evalTarget) config.Target {
	return config.Target{
		Name: "eval-" + string(tier), Provider: t.Provider, Model: pick(t.Model == "", "eval", t.Model),
		Locality: string(pick(tier == model.TierStrong, model.LocalityCloud, model.LocalityLocal)),
		Tier:     string(tier), ContextWindow: evalContextWindow, Pricing: t.Pricing,
	}
}

func routeScores(ctx context.Context, pl *route.Pipeline, routeName string, items []datasetItem) ([]float64, error) {
	scores := make([]float64, len(items))
	for i, it := range items {
		req, err := it.request(routeName)
		if err != nil {
			return nil, err
		}
		out, err := pl.Chat(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("route item %s: %w", it.ID, err)
		}
		d := out.Decision
		switch {
		case d.Score != nil:
			scores[i] = *d.Score
		case d.Tier == model.TierStrong:
			scores[i] = 2 // pinned to strong by a rule or the default tier
		default:
			scores[i] = -1
		}
	}
	return scores, nil
}

// stubDispatcher answers every plan from its primary target without any
// upstream call: pass 2 only needs the routing decision.
type stubDispatcher struct{}

func (stubDispatcher) Complete(_ context.Context, p dispatch.Plan) (dispatch.Result, error) {
	if len(p.Chain) == 0 {
		return dispatch.Result{}, dispatch.ErrNoTarget
	}
	t := p.Chain[0]
	return dispatch.Result{
		Target: t, Model: t.Model,
		Response: chat.Response{Model: t.Model, Choices: []chat.Choice{{
			Message: chat.TextMessage(chat.RoleAssistant, ""), FinishReason: chat.FinishStop,
		}}},
		Attempts: []decision.Attempt{{Target: t.Name, Model: t.Model, Locality: t.Locality, Status: "ok"}},
	}, nil
}

func (s stubDispatcher) Stream(ctx context.Context, p dispatch.Plan, _ dispatch.Sink) (dispatch.Result, error) {
	return s.Complete(ctx, p)
}

type decisionSink struct{}

func (decisionSink) Record(decision.Decision) {}

func (r *evalRun) writeCSV(res []itemResult, results []strategyResult) error {
	f, err := os.Create(filepath.Join(r.ec.OutDir, "results.csv"))
	if err != nil {
		return fmt.Errorf("results.csv: %w", err)
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{
		"strategy", "item_id", "score", "tier", "weak_correct", "strong_correct", "correct",
		"cost_usd",
	})
	for _, sr := range results {
		if sr.err != nil {
			continue
		}
		for i, it := range res {
			strong := routedStrong(sr.scores[i], sr.threshold)
			_ = w.Write([]string{
				sr.name, it.id, strconv.FormatFloat(sr.scores[i], 'f', 6, 64),
				string(pick(strong, model.TierStrong, model.TierWeak)),
				strconv.FormatBool(it.weakCorrect), strconv.FormatBool(it.strongCorrect),
				strconv.FormatBool(pick(strong, it.strongCorrect, it.weakCorrect)),
				strconv.FormatFloat(pick(strong, it.strongCost, it.weakCost), 'f', 8, 64),
			})
		}
	}
	w.Flush()
	return errors.Join(w.Error(), f.Close())
}

func (r *evalRun) writeSummary(res []itemResult, results []strategyResult) error {
	var b strings.Builder
	allStrong := 0.0
	for _, it := range res {
		allStrong += it.strongCost
	}
	fmt.Fprintf(&b, "# Evaluation %s\n\nMode %s, route %s, %d items, bootstrap %d resamples (seed %d).\n\n",
		filepath.Base(r.ec.OutDir), r.ec.Mode, r.ec.Route, len(res), r.ec.Bootstrap.Resamples, r.ec.Bootstrap.Seed)
	b.WriteString("| strategy | threshold | strong calls | accuracy (95% CI) | random at same ratio | " +
		"cost USD (95% CI) | cost vs all-strong | APGR (95% CI) |\n|---|---|---|---|---|---|---|---|\n")
	for _, sr := range results {
		if sr.err != nil {
			fmt.Fprintf(&b, "| %s | %v |  |  |  |  |  |  |\n", sr.name, sr.err)
			continue
		}
		s := r.summarise(res, sr)
		fmt.Fprintf(&b, "| %s | %.4f | %.1f%% | %.3f (%.3f-%.3f) | %.3f | %.4f (%.4f-%.4f) | %s | %.3f (%.3f-%.3f) |\n",
			sr.name, sr.threshold, 100*s.strongRatio, s.accuracy, s.accCI[0], s.accCI[1], s.randomAcc,
			s.cost, s.costCI[0], s.costCI[1], ratio(s.cost, allStrong), s.apgr, s.apgrCI[0], s.apgrCI[1])
	}
	accWeak, accStrong := tierAccuracy(res)
	var allWeak float64
	for _, it := range res {
		allWeak += it.weakCost
	}
	fmt.Fprintf(&b, "\nReference: all-weak accuracy %.3f (cost %.4f USD), all-strong accuracy %.3f (cost %.4f USD).\n",
		accWeak, allWeak, accStrong, allStrong)
	if r.ec.ThresholdRule.Calibration != "" {
		fmt.Fprintf(&b, "Threshold: lowest strong-call ratio on %s (%d items) with accuracy >= %.0f%% of all-strong.\n",
			displayPath(r.ec.ThresholdRule.Calibration), len(r.cal), 100*r.ec.ThresholdRule.MinAccuracyOfAllStrong)
	}
	for _, sr := range results {
		if sr.err == nil {
			writeSweep(&b, sr)
		}
	}
	if err := os.WriteFile(filepath.Join(r.ec.OutDir, "summary.md"), []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("summary.md: %w", err)
	}
	_, err := io.WriteString(r.cli.Stdout, b.String())
	return err //nolint:wrapcheck // stdout write
}

func ratio(a, b float64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*a/b)
}

// writeSweep adds one strategy's threshold sweep to summary.md.
func writeSweep(b *strings.Builder, sr strategyResult) {
	fmt.Fprintf(b, "\n## Sweep: %s\n\n", sr.name)
	b.WriteString("| threshold | strong calls | accuracy (95% CI) | random at same ratio | cost USD | PGR |\n" +
		"|---|---|---|---|---|---|\n")
	for _, p := range sr.curve {
		mark := pick(p.chosen, " (chosen)", "")
		fmt.Fprintf(b, "| %.4f%s | %.1f%% | %.3f (%.3f-%.3f) | %.3f | %.4f | %.3f |\n",
			p.threshold, mark, 100*p.strongRatio, p.accuracy, p.accCI[0], p.accCI[1], p.randomAcc, p.cost, p.pgr)
	}
}

// writeCurve writes curve.csv: every strategy's sweep, the input of research/plot_eval.py.
func (r *evalRun) writeCurve(results []strategyResult) error {
	f, err := os.Create(filepath.Join(r.ec.OutDir, "curve.csv"))
	if err != nil {
		return fmt.Errorf("curve.csv: %w", err)
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{
		"strategy", "threshold", "strong_ratio", "accuracy", "accuracy_ci_lo", "accuracy_ci_hi",
		"cost_usd", "random_accuracy", "pgr", "chosen",
	})
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	for _, sr := range results {
		for _, p := range sr.curve {
			_ = w.Write([]string{
				sr.name, num(p.threshold), num(p.strongRatio), num(p.accuracy), num(p.accCI[0]), num(p.accCI[1]),
				strconv.FormatFloat(p.cost, 'f', 8, 64), num(p.randomAcc), num(p.pgr), strconv.FormatBool(p.chosen),
			})
		}
	}
	w.Flush()
	return errors.Join(w.Error(), f.Close())
}

// displayPath shortens a path for the summary, which is meant to be committed and shared: an
// absolute path becomes relative to the working directory when it lies below it, and its base name
// otherwise, so no host-specific directory is written. Relative paths are kept as given.
func displayPath(p string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	if wd, err := os.Getwd(); err == nil {
		rel, err := filepath.Rel(wd, p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return filepath.Base(p)
}
