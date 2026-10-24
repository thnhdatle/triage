// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/thnhdatle/triage/internal/adapter/piionnx"
	"github.com/thnhdatle/triage/internal/adapter/strategy/length"
	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/platform/hftok"
)

const (
	strategyNone   = "none"
	strategyRandom = "random"
	strategyLength = length.Name
	strategyBERT   = "bert"
	strategyMF     = mf.Name
)

type plan struct {
	cacheDir     string
	strategies   map[string]string
	tokenizer    string
	bert         *bertFiles
	mfVector     string
	mfKey        string
	mfFallback   string
	ner          *nerFiles
	mfCostTarget string
	warnings     []string
}

type nerFiles struct {
	files piionnx.Files
	key   string
}

func (p *plan) warn(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

func usesStrategy(cfg *config.Config, name string) bool {
	return slices.ContainsFunc(cfg.Routes, func(r config.Route) bool { return r.Strategy == name })
}

// resolve checks cfg against the artifacts and the environment.
func resolve(ctx context.Context, cfg *config.Config, env Env) (*plan, error) {
	var problems []string
	add := func(path string, err error) { problems = append(problems, path+": "+err.Error()) }
	cacheDir, err := env.expandHome(cfg.Models.CacheDir)
	if err != nil {
		return nil, err
	}
	arts, err := loadArtifacts(ctx, cacheDir)
	if err != nil {
		return nil, err
	}
	p := &plan{cacheDir: cacheDir, strategies: map[string]string{}}
	fallback := cfg.Difficulty.MissingArtifact == "fallback"

	bertErr := p.resolveBERT(cfg, arts)
	mfErr := p.resolveMF(cfg, arts, env)
	problems = append(problems, p.routeStrategies(cfg, fallback, bertErr, mfErr)...)
	p.resolveMFFallback(cfg, bertErr)
	if err := p.resolveTokenizer(cfg, arts, env, fallback); err != nil {
		add("selection.tokenizer", err)
	}
	if err := p.resolveNER(cfg, arts); err != nil {
		add("privacy.ner", err)
	}
	if !p.needsBERT() {
		p.bert = nil
	}
	if len(problems) > 0 {
		return nil, &config.ValidationError{Problems: problems}
	}
	return p, nil
}

func (p *plan) routeStrategies(cfg *config.Config, fallback bool, bertErr, mfErr error) []string {
	var problems []string
	for _, r := range cfg.Routes {
		s := r.Strategy
		missing := map[string]error{strategyBERT: bertErr, strategyMF: mfErr}[s]
		switch {
		case missing == nil:
		case !fallback || errors.Is(missing, errEgress):
			problems = append(problems, fmt.Sprintf("routes (%s).strategy: %v", r.Name, missing))
			continue
		default:
			p.warn("route %s: strategy %s unavailable (%v); using length (difficulty.missing_artifact: fallback)",
				r.Name, s, missing)
			s = strategyLength
		}
		p.strategies[r.Name] = s
	}
	return problems
}

func (p *plan) resolveMFFallback(cfg *config.Config, bertErr error) {
	if !slices.Contains(mapValues(p.strategies), strategyMF) {
		return
	}
	p.mfFallback = strategyLength
	switch {
	case cfg.Difficulty.MF.Fallback != strategyBERT:
	case bertErr == nil:
		p.mfFallback = strategyBERT
	default:
		p.warn("difficulty.mf.fallback: bert unavailable (%v); mf falls back to length", bertErr)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (p *plan) needsBERT() bool {
	return slices.Contains(mapValues(p.strategies), strategyBERT) || p.mfFallback == strategyBERT
}

var errNoONNX = errors.New("this binary was built without ONNX Runtime (-tags noonnx or CGO_ENABLED=0); " +
	"use a release build or strategy length")

var errEgress = errors.New("mf data egress")

func (p *plan) resolveBERT(cfg *config.Config, arts *artifacts) error {
	needed := usesStrategy(cfg, strategyBERT) ||
		(usesStrategy(cfg, strategyMF) && cfg.Difficulty.MF.Fallback == strategyBERT)
	if !needed {
		return nil
	}
	if !onnxBuilt {
		return fmt.Errorf("strategy bert: %w", errNoONNX)
	}
	files, err := arts.bert(cfg.Difficulty.BERT.Variant)
	if err != nil {
		return err
	}
	p.bert = &files
	return nil
}

func (p *plan) resolveMF(cfg *config.Config, arts *artifacts, env Env) error {
	if !usesStrategy(cfg, strategyMF) {
		return nil
	}
	m := cfg.Difficulty.MF
	pr := cfg.Providers[m.Provider]
	if _, err := env.apiKey(pr.APIKeyEnv, pr.APIKeyFile); err != nil || (pr.APIKeyEnv == "" && pr.APIKeyFile == "") {
		if err == nil {
			err = errNoKey
		}
		return fmt.Errorf("%w: difficulty.mf.provider %q needs an API key: %w", errEgress, m.Provider, err)
	}
	for _, t := range cfg.Targets {
		if t.Provider == m.Provider && t.Model == mf.DefaultModel {
			p.mfCostTarget = t.Name
			break
		}
	}
	if p.mfCostTarget == "" {
		p.warn("no target with provider %q and model %s: mf scoring cost is recorded as 0",
			m.Provider, mf.DefaultModel)
	}
	path, key, err := arts.mfVector()
	if err != nil {
		return err
	}
	p.mfVector, p.mfKey = path, key
	return nil
}

func (p *plan) resolveTokenizer(cfg *config.Config, arts *artifacts, env Env, fallback bool) error {
	var path string
	var err error
	switch tok := cfg.Selection.Tokenizer; tok {
	case "estimate":
		return nil
	case strategyBERT:
		path, err = arts.bertTokenizer()
	default:
		path, err = env.expandHome(tok)
		if err == nil {
			if _, serr := os.Stat(path); serr != nil {
				err = fmt.Errorf("tokenizer file %s: %w", path, serr)
			}
		}
	}
	if err == nil {
		if _, lerr := hftok.Load(path); lerr != nil {
			err = fmt.Errorf("tokenizer file %s: %w", path, lerr)
		}
	}
	if err != nil {
		if fallback {
			p.warn("selection.tokenizer unavailable (%v); using estimate (difficulty.missing_artifact: fallback)", err)
			return nil
		}
		return err
	}
	p.tokenizer = path
	return nil
}

func (p *plan) resolveNER(cfg *config.Config, arts *artifacts) error {
	if !cfg.Privacy.NER.Enabled {
		return nil
	}
	if !onnxBuilt {
		return fmt.Errorf("enabled: %w", errNoONNX)
	}
	files, key, err := arts.ner()
	if err != nil {
		return err
	}
	p.ner = &nerFiles{files: files, key: key}
	return nil
}
