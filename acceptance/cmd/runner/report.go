// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// Status is the result of one report row.
type Status string

// Row statuses.
const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusSkipped Status = "Skipped (environment)"
	StatusBlocked Status = "Blocked (environment)"
)

// Row is one line of the report table.
type Row struct {
	ID        string
	Status    Status
	Measured  string
	Threshold string
	Note      string
}

// NotMet is the optional explanation of a FAIL row (acceptance/not_met.yaml).
type NotMet struct {
	Cause string `yaml:"cause"`
}

// Header carries the report title fields.
type Header struct {
	Project string
	Version string
	Date    string
	Env     string
}

// formatMap renders a map as sorted "k=v" pairs.
func formatMap(m map[string]any) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

// renderTable renders only the markdown table.
func renderTable(rows []Row) string {
	var b strings.Builder
	b.WriteString("| ID | Result | Measured | Threshold |\n|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			cell(r.ID), r.Status, cell(r.Measured), cell(r.Threshold))
	}
	return b.String()
}

func renderSection(b *strings.Builder, title string, lines []string) {
	fmt.Fprintf(b, "## %s\n", title)
	if len(lines) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
}

func notMetLine(r Row, nm map[string]NotMet) string {
	n, ok := nm[r.ID]
	if !ok {
		return fmt.Sprintf("- %s: no explanation in not_met.yaml", r.ID)
	}
	return fmt.Sprintf("- %s: %s", r.ID, n.Cause)
}

// renderReport renders acceptance/report.md in the format described in acceptance/README.md.
func renderReport(h Header, rows []Row, nm map[string]NotMet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Acceptance report — %s %s — %s\nEnvironment: %s\n", h.Project, h.Version, h.Date, h.Env)
	b.WriteString(renderTable(rows))
	var notMet, skipped, blocked []string
	for _, r := range rows {
		switch r.Status {
		case StatusFail:
			notMet = append(notMet, notMetLine(r, nm))
		case StatusSkipped:
			skipped = append(skipped, fmt.Sprintf("- %s: %s", r.ID, r.Note))
		case StatusBlocked:
			blocked = append(blocked, fmt.Sprintf("- %s: %s", r.ID, r.Note))
		case StatusPass:
		}
	}
	renderSection(&b, "Not met", notMet)
	renderSection(&b, "Skipped (environment)", skipped)
	renderSection(&b, "Blocked (environment)", blocked)
	return b.String()
}

// summarize returns the one-line summary and whether the run failed.
func summarize(rows []Row) (string, bool) {
	counts := map[Status]int{}
	for _, r := range rows {
		counts[r.Status]++
	}
	failed := counts[StatusFail] > 0
	word := "PASS"
	if failed {
		word = "FAIL"
	}
	return fmt.Sprintf("%s acceptance: %d passed, %d failed, %d skipped, %d blocked",
		word, counts[StatusPass], counts[StatusFail], counts[StatusSkipped], counts[StatusBlocked]), failed
}
