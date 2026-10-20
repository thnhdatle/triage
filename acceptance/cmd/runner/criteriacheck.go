// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/thnhdatle/triage/acceptance"
)

var (
	testFuncRe = regexp.MustCompile(`(?m)^func ((?:Test|Benchmark)\w*)\(`)
	skipRe     = regexp.MustCompile(`\.Skip(?:f|Now)?\(|\bSkipf\b|SkipNow|testing\.Short|` +
		`pytest\.mark\.skip|pytest\.skip|xfail`)
	acceptanceNameRe = regexp.MustCompile(`^(?:Test|Benchmark)Acceptance_`)
)

// loadSources reads acceptance sources (.go, .py) below dir, excluding cmd/
// (this runner), logs/, and installed third-party code (node_modules/ and
// hidden directories such as a uv .venv/), which is not part of the suite.
func loadSources(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			n := d.Name()
			if path != dir && (n == "cmd" || n == "logs" || n == "node_modules" || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".go" && ext != ".py" {
			return nil
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // path comes from walking the acceptance dir
		if rerr != nil {
			return rerr
		}
		out[path] = string(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	return out, nil
}

// checkCriteria verifies every criterion has exactly one Test/Benchmark named by its test field
// and that no stray acceptance test exists. sources maps file path to content.
func checkCriteria(f *acceptance.File, sources map[string]string) []string {
	found := map[string][]string{}
	for path, src := range sources {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, m := range testFuncRe.FindAllStringSubmatch(src, -1) {
			found[m[1]] = append(found[m[1]], filepath.Base(path))
		}
	}
	var problems []string
	want := map[string]bool{}
	for _, c := range f.Criteria {
		want[c.Test] = true
		if n := len(found[c.Test]); n != 1 {
			problems = append(problems, fmt.Sprintf("%s: %d tests named %q (want exactly 1)", c.ID, n, c.Test))
		}
	}
	for name := range found {
		if acceptanceNameRe.MatchString(name) && !want[name] {
			problems = append(problems, fmt.Sprintf("test %q matches no criterion", name))
		}
	}
	sort.Strings(problems)
	return problems
}

// scanSkips returns file:line hits for skip/xfail constructs in acceptance sources. A criterion
// whose environment is missing is reported as Skipped or Blocked by the runner, so the tests
// themselves have no skip path.
func scanSkips(sources map[string]string) []string {
	var hits []string
	for path, src := range sources {
		for i, line := range strings.Split(src, "\n") {
			if m := skipRe.FindString(line); m != "" {
				hits = append(hits, fmt.Sprintf("%s:%d: skip construct %q", path, i+1, m))
			}
		}
	}
	sort.Strings(hits)
	return hits
}

// criteriaRule is what the criteria check verifies.
const criteriaRule = "one test per criterion, no skip constructs"

// criteriaRow converts the findings of the criteria check to the "criteria" report row.
func criteriaRow(problems []string) Row {
	if len(problems) == 0 {
		return Row{ID: "criteria", Status: StatusPass, Measured: criteriaRule, Threshold: "-"}
	}
	return Row{
		ID: "criteria", Status: StatusFail, Measured: strings.Join(problems, "; "),
		Threshold: criteriaRule,
	}
}
