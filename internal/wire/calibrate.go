// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/thnhdatle/triage/internal/adapter/decisionlog"
	"github.com/thnhdatle/triage/internal/app/calibrate"
	"github.com/thnhdatle/triage/internal/app/health"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

func (c *CLI) calibrate(ctx context.Context, args []string) error {
	fs := c.flags("calibrate")
	path := fs.String("config", "", "config file (default $"+EnvConfig+")")
	routeName := fs.String("route", "", "route whose strategy is calibrated (required)")
	ratio := fs.Float64("strong-ratio", 0, "target fraction of requests routed to strong, in [0,1] (required)")
	sample := fs.String("sample", "", "JSONL sample of requests: messages or prompt per line (required)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *routeName == "" || *sample == "" || !(*ratio >= 0 && *ratio <= 1) {
		return usagef("calibrate needs --route, --sample and --strong-ratio in [0,1]")
	}
	cfg, items, err := c.calibrationInputs(*path, *sample)
	if err != nil {
		return err
	}
	ob, err := newOffline(ctx, c.Env, c.logger(), cfg)
	if err != nil {
		return err
	}
	defer ob.close(ctx)
	g, err := ob.b.build(ctx, cfg)
	if err != nil {
		return err
	}
	defer ob.b.retire(ctx, g)
	name, ok := g.plan.strategies[*routeName]
	if !ok {
		return usagef("unknown route %q", *routeName)
	}
	s := g.strategies[name]
	if s == nil {
		return usagef("route %q uses strategy %q: nothing to calibrate", *routeName, name)
	}
	reqs := make([]chat.Request, 0, len(items))
	for _, it := range items {
		r, err := it.request(*routeName)
		if err != nil {
			return asUsage(err)
		}
		reqs = append(reqs, r)
	}
	res, err := calibrate.Calibrate(ctx, s, reqs, *ratio)
	if err != nil {
		return fmt.Errorf("calibrate: %w", err)
	}
	_, _ = fmt.Fprintf(c.Stderr, "strategy %s on %d requests: threshold %.4f gives strong ratio %.3f (target %.3f)\n",
		res.Strategy, res.N, float64(res.Threshold), res.AchievedRatio, *ratio)
	_, err = fmt.Fprint(c.Stdout, res.Snippet(*routeName))
	return err //nolint:wrapcheck // stdout write
}

func (c *CLI) calibrationInputs(path, sample string) (*config.Config, []datasetItem, error) {
	p, err := c.configPath(path)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return nil, nil, asUsage(err)
	}
	items, err := readDataset(sample)
	if err != nil {
		return nil, nil, asUsage(err)
	}
	return cfg, items, nil
}

// offline is a builder for commands that score or route without serving.
type offline struct {
	b   *builder
	tel *telemetry.Telemetry
}

func newOffline(ctx context.Context, env Env, log *slog.Logger, cfg *config.Config) (*offline, error) {
	lib, err := env.expandHome(cfg.ONNX.LibraryPath)
	if err != nil {
		return nil, err
	}
	tel, err := telemetry.Setup(ctx, telemetry.Options{})
	if err != nil {
		return nil, err
	}
	clk := clock.Real()
	tr, err := health.New(clk, health.Options{})
	if err != nil {
		return nil, err
	}
	dlog, err := decisionlog.New(1)
	if err != nil {
		return nil, err
	}
	return &offline{tel: tel, b: &builder{
		env: env, log: log, tel: tel, clk: clk, health: tr, recorder: dlog, ort: &ortHolder{path: lib, log: log},
	}}, nil
}

func (o *offline) close(ctx context.Context) {
	_ = o.b.ort.close()
	_ = o.tel.Shutdown(ctx)
}
