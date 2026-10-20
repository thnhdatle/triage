// SPDX-License-Identifier: Apache-2.0

// Command runner runs the acceptance suite behind `make acceptance`: it builds the project, starts the
// compose stack, probes each criterion's requirements, runs its test and writes the report.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/acceptance"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	only     string
	version  string
	criteria string
	report   string
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.only, "only", "", "comma-separated criterion ids to run (default: all)")
	fs.StringVar(&o.version, "version", "dev", "version shown in the report heading")
	fs.StringVar(&o.criteria, "criteria", "acceptance/criteria.yaml", "path to criteria.yaml")
	fs.StringVar(&o.report, "report", "acceptance/report.md", "path of the generated report")
	if err := fs.Parse(args); err != nil {
		return o, fmt.Errorf("parse flags: %w", err)
	}
	return o, nil
}

// selectCriteria filters by the --only list and orders requires:lmstudio criteria last.
func selectCriteria(f *acceptance.File, only string) ([]*acceptance.Criterion, error) {
	var ids []string
	for _, id := range strings.Split(only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if _, ok := f.Find(id); !ok {
			return nil, fmt.Errorf("unknown criterion id %q", id)
		}
	}
	var first, last []*acceptance.Criterion
	for i := range f.Criteria {
		c := &f.Criteria[i]
		if len(ids) > 0 && !slices.Contains(ids, c.ID) {
			continue
		}
		if slices.Contains(c.Requires, reqLMStudio) {
			last = append(last, c)
		} else {
			first = append(first, c)
		}
	}
	return append(first, last...), nil
}

// environment returns "ci" when CI=1 and "mac" otherwise.
func environment() string {
	if os.Getenv("CI") == "1" {
		return acceptance.EnvCI
	}
	return acceptance.EnvMac
}

func loadNotMet(path string) map[string]NotMet {
	data, err := os.ReadFile(path) //nolint:gosec // fixed optional file next to the report
	if err != nil {
		return nil
	}
	var m map[string]NotMet
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// evaluate probes requirements and runs one criterion.
func evaluate(c *acceptance.Criterion, env string, p *prober, w io.Writer) Row {
	thr, skip := c.Thresholds(env)
	if skip {
		return Row{
			ID: c.ID, Status: StatusSkipped, Threshold: "skip",
			Note: fmt.Sprintf("no %s thresholds (pass.%s: skip)", env, env),
		}
	}
	ordered := slices.SortedStableFunc(slices.Values(c.Requires), func(a, b string) int {
		return boolInt(a == reqLMStudio) - boolInt(b == reqLMStudio)
	})
	for _, req := range ordered {
		if req == reqLMStudio {
			prepareStack(c, w)
		}
		if res := p.probe(req); !res.OK {
			st := StatusSkipped
			if isBlocking(req) {
				st = StatusBlocked
			}
			return Row{
				ID: c.ID, Status: st, Threshold: formatMap(thr),
				Note: fmt.Sprintf("requires %s — probe: %s", req, res.Detail),
			}
		}
	}
	if !slices.Contains(c.Requires, reqLMStudio) {
		prepareStack(c, w)
	}
	return execute(c, env, thr)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}
	f, err := acceptance.Load(o.criteria)
	if err != nil {
		say(stderr, "%v\n", err)
		return 2
	}
	list, err := selectCriteria(f, o.only)
	if err != nil {
		say(stderr, "%v\n", err)
		return 2
	}
	env := environment()
	rows := preflight(f, stderr)
	rows = append(rows, buildStep(stderr)...)
	useLocalImages()
	_ = os.Setenv("TRIAGE_ACCEPTANCE_VERSION", o.version) // recorded by tests that write result files
	rows = append(rows, composeUp(stderr)...)
	p := newProber(env == acceptance.EnvCI)
	for _, c := range list {
		say(stderr, "== %s %s\n", c.ID, c.Title)
		rows = append(rows, evaluate(c, env, p, stderr))
	}
	hdr := Header{Project: f.Project, Version: o.version, Date: time.Now().UTC().Format("2006-01-02"), Env: env}
	rep := renderReport(hdr, rows, loadNotMet("acceptance/not_met.yaml"))
	if err := os.WriteFile(o.report, []byte(rep), 0o600); err != nil {
		say(stderr, "write report: %v\n", err)
		return 2
	}
	summary, failed := summarize(rows)
	say(stdout, "%s\n", summary)
	say(stdout, "%s", renderTable(rows))
	if failed {
		return 1
	}
	return 0
}

// preflight checks that every criterion has exactly one test and that no test has a skip construct.
func preflight(f *acceptance.File, stderr io.Writer) []Row {
	sources, err := loadSources("acceptance")
	if err != nil {
		say(stderr, "%v\n", err)
		return []Row{criteriaRow([]string{err.Error()})}
	}
	return []Row{criteriaRow(append(checkCriteria(f, sources), scanSkips(sources)...))}
}
