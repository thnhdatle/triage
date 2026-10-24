// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/platform/buildinfo"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const EnvConfig = "TRIAGE_CONFIG"

const defaultDataAddr = "127.0.0.1:8080"

const healthcheckTimeout = 3 * time.Second

const usage = `usage: triage <command> [flags]

commands:
  serve        run the router (--config)
  models       pull [bert|mf|ner|all] [--int8] | list
  calibrate    --route NAME --strong-ratio R --sample FILE.jsonl
  eval         --config eval.yaml
  config       validate | print [--redact]
  healthcheck  exit 0 when /readyz returns 200
  version      print the build information
`

// CLI runs the triage command line.
type CLI struct {
	Stdout, Stderr io.Writer
	Env            Env
	Reload         <-chan struct{}
	Logger         *slog.Logger
	Serve          ServeOptions
}

type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return &usageError{err: fmt.Errorf(format, args...)} //nolint:err113 // formatted usage messages
}

func asUsage(err error) error {
	if err == nil {
		return nil
	}
	return &usageError{err: err}
}

// Run executes args (without the program name) and returns the exit code.
func (c *CLI) Run(ctx context.Context, args []string) int {
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	if len(args) == 0 {
		_, _ = fmt.Fprint(c.Stderr, usage)
		return ExitUsage
	}
	err := c.dispatch(ctx, args[0], args[1:])
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	case isUsage(err) || isValidation(err):
		_, _ = fmt.Fprintln(c.Stderr, "triage:", err)
		return ExitUsage
	default:
		_, _ = fmt.Fprintln(c.Stderr, "triage:", err)
		return ExitFailure
	}
}

func isUsage(err error) bool {
	var ue *usageError
	return errors.As(err, &ue)
}

func isValidation(err error) bool {
	var ve *config.ValidationError
	return errors.As(err, &ve)
}

func (c *CLI) dispatch(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "serve":
		return c.serve(ctx, args)
	case "models":
		return c.models(ctx, args)
	case "calibrate":
		return c.calibrate(ctx, args)
	case "eval":
		return c.eval(ctx, args)
	case "config":
		return c.config(ctx, args)
	case "healthcheck":
		return c.healthcheck(ctx, args)
	case "version", "--version", "-v":
		info := buildinfo.Get()
		_, err := fmt.Fprintf(c.Stdout, "triage %s (%s, %s) %s\n", info.Version, info.Commit, info.BuildDate, info.GoVersion)
		return err //nolint:wrapcheck // stdout write
	case "help", "--help", "-h":
		_, err := fmt.Fprint(c.Stdout, usage)
		return err //nolint:wrapcheck // stdout write
	}
	return usagef("unknown command %q\n%s", cmd, usage)
}

func (c *CLI) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("triage "+name, flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err //nolint:wrapcheck // sentinel checked by Run
		}
		return asUsage(err)
	}
	return nil
}

func (c *CLI) configPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := c.Env.getenv(EnvConfig); v != "" {
		return v, nil
	}
	return "", usagef("no configuration: pass --config or set %s", EnvConfig)
}

func (c *CLI) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(c.Env.getenv("TRIAGE_LOG_LEVEL"))); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(c.Env.getenv("TRIAGE_LOG_FORMAT"), "text") {
		return slog.New(slog.NewTextHandler(c.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(c.Stderr, opts))
}

func (c *CLI) serve(ctx context.Context, args []string) error {
	fs := c.flags("serve")
	path := fs.String("config", "", "config file (default $"+EnvConfig+")")
	if err := parse(fs, args); err != nil {
		return err
	}
	p, err := c.configPath(*path)
	if err != nil {
		return err
	}
	o := c.Serve
	o.ConfigPath, o.Env, o.Reload = p, c.Env, c.Reload
	if o.Logger == nil {
		o.Logger = c.logger()
	}
	return Serve(ctx, o)
}

func (c *CLI) config(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("config validate | print [--redact]")
	}
	fs := c.flags("config " + args[0])
	path := fs.String("config", "", "config file (default $"+EnvConfig+")")
	redact := fs.Bool("redact", false, "replace extra_headers values and URL credentials")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	p, err := c.configPath(*path)
	if err != nil {
		return err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return asUsage(err)
	}
	switch args[0] {
	case "validate":
		pl, err := resolve(ctx, cfg, c.Env)
		if err != nil {
			return err
		}
		for _, w := range pl.warnings {
			_, _ = fmt.Fprintln(c.Stderr, "warning:", w)
		}
		_, err = fmt.Fprintf(c.Stdout, "%s: ok\n", p)
		return err //nolint:wrapcheck // stdout write
	case "print":
		if *redact {
			cfg = cfg.Redacted()
		}
		enc := yaml.NewEncoder(c.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(cfg); err != nil {
			return fmt.Errorf("print config: %w", err)
		}
		return enc.Close() //nolint:wrapcheck // stdout write
	}
	return usagef("unknown config command %q", args[0])
}

func (c *CLI) healthcheck(ctx context.Context, args []string) error {
	fs := c.flags("healthcheck")
	url := fs.String("url", "", "readiness URL (default: from the config's server.listen)")
	path := fs.String("config", "", "config file (default $"+EnvConfig+", then /etc/triage/config.yaml)")
	if err := parse(fs, args); err != nil {
		return err
	}
	target := *url
	if target == "" {
		target = "http://" + c.dataAddr(*path) + "/readyz"
	}
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return asUsage(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck %s: %w", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck %s: status %d", target, resp.StatusCode)
	}
	return nil
}

func (c *CLI) dataAddr(flagPath string) string {
	p := flagPath
	if p == "" {
		p = c.Env.getenv(EnvConfig)
	}
	if p == "" {
		p = "/etc/triage/config.yaml"
	}
	cfg, err := config.Load(p)
	if err != nil {
		return defaultDataAddr
	}
	return dialable(cfg.Server.Listen)
}

func dialable(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return defaultDataAddr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
