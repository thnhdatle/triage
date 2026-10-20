// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/thnhdatle/triage/internal/adapter/artifactstore"
	"github.com/thnhdatle/triage/internal/adapter/hfhub"
	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	appartifact "github.com/thnhdatle/triage/internal/app/artifact"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/artifact"
	pinned "github.com/thnhdatle/triage/models"
)

// Converter image defaults.
const (
	envModelsImage     = "RAYLD_TRIAGE_MODELS_IMAGE"
	defaultModelsImage = "ghcr.io/thnhdatle/triage-models"
	envResearchDir     = "TRIAGE_RESEARCH_DIR"
)

type modelsFlags struct {
	config, cacheDir, researchDir string
	int8                          bool
}

func (c *CLI) models(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "pull" && args[0] != "list") {
		return usagef("models pull [bert|mf|ner|all] [--int8] | models list")
	}
	fs := c.flags("models " + args[0])
	var f modelsFlags
	fs.StringVar(&f.config, "config", "", "config file (default $"+EnvConfig+"); selects the cache dir and, "+
		"for pull without names, the artifacts the routes need")
	fs.StringVar(&f.cacheDir, "cache-dir", "", "artifact cache (default models.cache_dir)")
	fs.StringVar(&f.researchDir, "research-dir", "", "research/ checkout for the INT8 converter (default $"+
		envResearchDir+" or ./research)")
	fs.BoolVar(&f.int8, "int8", false, "also build the INT8 bert model (uv or the converter image)")
	names, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	cfg, err := c.optionalConfig(f.config)
	if err != nil {
		return err
	}
	svc, err := c.artifactService(cfg, f)
	if err != nil {
		return err
	}
	if args[0] == "list" {
		return c.modelsList(ctx, svc)
	}
	if len(names) == 0 {
		if cfg == nil {
			return usagef("models pull: name the artifacts (bert, mf, ner, all) or pass --config")
		}
		names = neededArtifacts(cfg)
		if len(names) == 0 {
			_, err := fmt.Fprintln(c.Stdout, "the configuration needs no model artifacts")
			return err //nolint:wrapcheck // stdout write
		}
	}
	return c.modelsPull(ctx, svc, names, f.int8)
}

// parseInterspersed parses flags and positional arguments in any order.
func parseInterspersed(fs interface {
	Parse([]string) error
	Args() []string
}, args []string,
) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, asUsage(err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func (c *CLI) optionalConfig(flagPath string) (*config.Config, error) {
	p := flagPath
	if p == "" {
		p = c.Env.getenv(EnvConfig)
	}
	if p == "" {
		return nil, nil //nolint:nilnil // no configuration is a valid state for models
	}
	cfg, err := config.Load(p)
	if err != nil {
		return nil, asUsage(err)
	}
	return cfg, nil
}

// neededArtifacts lists the entries the configuration uses.
func neededArtifacts(cfg *config.Config) []string {
	var out []string
	if usesStrategy(cfg, strategyBERT) || cfg.Selection.Tokenizer == strategyBERT ||
		(usesStrategy(cfg, strategyMF) && cfg.Difficulty.MF.Fallback == strategyBERT) {
		out = append(out, entryBERT)
	}
	if usesStrategy(cfg, strategyMF) {
		out = append(out, entryMF)
	}
	if cfg.Privacy.NER.Enabled {
		out = append(out, entryNER)
	}
	return out
}

func (c *CLI) cacheDir(cfg *config.Config, flagValue string) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = config.Default().Models.CacheDir
		if cfg != nil {
			dir = cfg.Models.CacheDir
		}
	}
	return c.Env.expandHome(dir)
}

func (c *CLI) artifactService(cfg *config.Config, f modelsFlags) (*appartifact.Service, error) {
	dir, err := c.cacheDir(cfg, f.cacheDir)
	if err != nil {
		return nil, err
	}
	m, err := pinned.Manifest()
	if err != nil {
		return nil, err
	}
	fetcher, err := hfhub.New()
	if err != nil {
		return nil, fmt.Errorf("hfhub: %w", err)
	}
	store, err := artifactstore.New(dir)
	if err != nil {
		return nil, fmt.Errorf("artifact store: %w", err)
	}
	fp32, err := bert.NewFP32Deriver(pinned.BERTGraph())
	if err != nil {
		return nil, fmt.Errorf("bert fp32 deriver: %w", err)
	}
	image := c.Env.getenv(envModelsImage)
	if image == "" {
		image = defaultModelsImage
	}
	q8, err := bert.NewINT8Deriver(bert.SelectConverter(exec.LookPath, c.researchDir(f.researchDir), image))
	if err != nil {
		return nil, fmt.Errorf("bert int8 deriver: %w", err)
	}
	svc, err := appartifact.New(m, fetcher, []artifact.Deriver{fp32, q8, mf.Deriver{}}, store, dir)
	if err != nil {
		return nil, fmt.Errorf("artifacts: %w", err)
	}
	return svc, nil
}

func (c *CLI) researchDir(flagValue string) string {
	for _, d := range []string{flagValue, c.Env.getenv(envResearchDir), "research"} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "pyproject.toml")); err == nil {
			abs, err := filepath.Abs(d)
			if err == nil {
				return abs
			}
		}
	}
	return ""
}

func (c *CLI) modelsPull(ctx context.Context, svc *appartifact.Service, names []string, withINT8 bool) error {
	rep, err := svc.Pull(ctx, names, appartifact.PullOptions{INT8: withINT8})
	for _, e := range rep.Entries {
		_, _ = fmt.Fprintf(c.Stdout, "%s: fetched %d file(s), %d already present", e.Name, len(e.Fetched), len(e.Skipped))
		for _, d := range e.DerivedBuilt {
			_, _ = fmt.Fprintf(c.Stdout, "; built %s", d.Kind)
		}
		for _, k := range e.DerivedSkipped {
			_, _ = fmt.Fprintf(c.Stdout, "; %s up to date", k)
		}
		_, _ = fmt.Fprintf(c.Stdout, " (%s)\n", e.Duration.Round(1e6))
	}
	if err != nil {
		return fmt.Errorf("models pull: %w", err)
	}
	return nil
}

func (c *CLI) modelsList(ctx context.Context, svc *appartifact.Service) error {
	sts, err := svc.List(ctx)
	if err != nil {
		return fmt.Errorf("models list: %w", err)
	}
	w := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tREVISION\tFILES\tDERIVED\tACTIVE\tEXPECTED LATENCY (p50)\tSUGGESTED TIMEOUT")
	for _, s := range sts {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, short(s.Revision), filesSummary(s),
			derivedSummary(s), orDash(s.Active), latencySummary(s), orDash(s.SuggestedTimeout))
	}
	return w.Flush() //nolint:wrapcheck // stdout write
}

func short(rev string) string { return rev[:min(len(rev), 12)] }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func filesSummary(s appartifact.Status) string {
	ok := 0
	var bad []string
	for _, f := range s.Files {
		if f.State == appartifact.FileOK {
			ok++
			continue
		}
		bad = append(bad, f.Path+":"+string(f.State))
	}
	if len(bad) == 0 {
		return fmt.Sprintf("%d/%d verified", ok, len(s.Files))
	}
	return fmt.Sprintf("%d/%d verified (%s)", ok, len(s.Files), strings.Join(bad, ", "))
}

func derivedSummary(s appartifact.Status) string {
	parts := make([]string, 0, len(s.Derived))
	for _, d := range s.Derived {
		state := "missing"
		if d.Present {
			state = "built"
		}
		parts = append(parts, string(d.Kind)+":"+state)
	}
	if len(parts) == 0 {
		return "-"
	}
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

func latencySummary(s appartifact.Status) string {
	var parts []string
	for _, l := range s.Latency {
		if l.Variant == s.Active {
			parts = append(parts, fmt.Sprintf("%d tok %s", l.Tokens, l.P50))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}
