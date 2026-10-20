// SPDX-License-Identifier: Apache-2.0

// Package prompts resolves the benchmark prompt sets of bench/prompts (ids
// committed, texts read from the public datasets fetched by
// scripts/fetch_eval_data.sh) and selects them deterministically.
//
// Id grammar (the same as eval/README.md, without the GSM8K wildcard):
//
//	gsm8k/test:<row>             zero-based row of gsm8k/test.jsonl; text = question
//	mmlu/test:<subject>:<row>    zero-based row of mmlu/test/<subject>.jsonl; text =
//	                             question, "\nA. <choice>"..., "\nAnswer:" (RouteLLM's
//	                             MMLU prompt form, the text bert scores in TRG-10)
package prompts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Sources of the ids.
const (
	SourceGSM8K = "gsm8k/test"
	SourceMMLU  = "mmlu/test"
)

// maxLine bounds one JSONL line of the datasets.
const maxLine = 16 << 20

// mmluLetters label the MMLU choices.
const mmluLetters = "ABCD"

var subjectRE = regexp.MustCompile(`^[a-z0-9_]+$`)

// ErrBadID is returned for an id outside the grammar or the data.
var ErrBadID = errors.New("prompts: bad id")

// Prompt is one resolved prompt.
type Prompt struct {
	ID   string
	Text string
}

// ReadIDs reads an id file: one id per line, blank lines and lines starting
// with # ignored. Duplicates are an error (a set is a list of distinct items).
func ReadIDs(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: committed set file chosen by the caller
	if err != nil {
		return nil, fmt.Errorf("prompts: %w", err)
	}
	var ids []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		id := strings.TrimSpace(line)
		if id == "" || strings.HasPrefix(id, "#") {
			continue
		}
		if seen[id] {
			return nil, fmt.Errorf("%w: %q listed twice in %s", ErrBadID, id, path)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("prompts: %s lists no ids", path)
	}
	return ids, nil
}

// Data reads prompt texts from a converted eval data directory
// (gsm8k/test.jsonl, mmlu/test/<subject>.jsonl). Files are read once.
type Data struct {
	dir   string
	gsm   []string
	mmlu  map[string][]string
	subjs []string
}

// NewData returns a reader over dir.
func NewData(dir string) *Data { return &Data{dir: dir, mmlu: map[string][]string{}} }

// Resolve returns the prompts of ids, in order.
func (d *Data) Resolve(ids []string) ([]Prompt, error) {
	out := make([]Prompt, 0, len(ids))
	for _, id := range ids {
		text, err := d.Text(id)
		if err != nil {
			return nil, err
		}
		out = append(out, Prompt{ID: id, Text: text})
	}
	return out, nil
}

// Text returns the prompt text of one id.
func (d *Data) Text(id string) (string, error) {
	src, rest, ok := strings.Cut(id, ":")
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrBadID, id)
	}
	var (
		rows    []string
		rowText = rest
		err     error
	)
	switch src {
	case SourceGSM8K:
		rows, err = d.gsm8k()
	case SourceMMLU:
		sub, r, ok := strings.Cut(rest, ":")
		if !ok || !subjectRE.MatchString(sub) {
			return "", fmt.Errorf("%w: %q (want mmlu/test:<subject>:<row>)", ErrBadID, id)
		}
		rowText = r
		rows, err = d.subject(sub)
	default:
		return "", fmt.Errorf("%w: %q (unknown source)", ErrBadID, id)
	}
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(rowText)
	if err != nil || n < 0 || n >= len(rows) {
		return "", fmt.Errorf("%w: %q (row out of range, %d rows)", ErrBadID, id, len(rows))
	}
	return rows[n], nil
}

// All returns every prompt of the datasets: GSM8K test rows, then the MMLU
// subjects in lexical order.
func (d *Data) All() ([]Prompt, error) {
	gsm, err := d.gsm8k()
	if err != nil {
		return nil, err
	}
	out := make([]Prompt, 0, len(gsm))
	for i, t := range gsm {
		out = append(out, Prompt{ID: SourceGSM8K + ":" + strconv.Itoa(i), Text: t})
	}
	subjs, err := d.subjects()
	if err != nil {
		return nil, err
	}
	for _, s := range subjs {
		rows, err := d.subject(s)
		if err != nil {
			return nil, err
		}
		for i, t := range rows {
			out = append(out, Prompt{ID: SourceMMLU + ":" + s + ":" + strconv.Itoa(i), Text: t})
		}
	}
	return out, nil
}

// Select returns n prompts drawn without replacement, in a seeded random
// order, from the prompts of all whose count is within [minTokens, maxTokens].
// The result depends only on all's order, count, the bounds, n and seed.
func Select(all []Prompt, count func(string) int, minTokens, maxTokens, n int, seed uint64) ([]Prompt, error) {
	var pool []Prompt
	for _, p := range all {
		if c := count(p.Text); c >= minTokens && c <= maxTokens {
			pool = append(pool, p)
		}
	}
	if len(pool) < n {
		return nil, fmt.Errorf("prompts: only %d prompts with %d..%d tokens, want %d", len(pool), minTokens, maxTokens, n)
	}
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // G404: reproducible sample, not security
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return pool[:n], nil
}

func (d *Data) gsm8k() ([]string, error) {
	if d.gsm != nil {
		return d.gsm, nil
	}
	type row struct {
		Question string `json:"question"`
	}
	rows, err := readJSONL[row](filepath.Join(d.dir, "gsm8k", "test.jsonl"))
	if err != nil {
		return nil, err
	}
	d.gsm = make([]string, len(rows))
	for i, r := range rows {
		d.gsm[i] = r.Question
	}
	return d.gsm, nil
}

func (d *Data) subject(s string) ([]string, error) {
	if rows, ok := d.mmlu[s]; ok {
		return rows, nil
	}
	type row struct {
		Question string   `json:"question"`
		Choices  []string `json:"choices"`
	}
	rows, err := readJSONL[row](filepath.Join(d.dir, "mmlu", "test", s+".jsonl"))
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(rows))
	for i, r := range rows {
		if len(r.Choices) > len(mmluLetters) {
			return nil, fmt.Errorf("prompts: mmlu %s row %d has %d choices", s, i, len(r.Choices))
		}
		texts[i] = mmluPrompt(r.Question, r.Choices)
	}
	d.mmlu[s] = texts
	return texts, nil
}

func (d *Data) subjects() ([]string, error) {
	if d.subjs != nil {
		return d.subjs, nil
	}
	files, err := filepath.Glob(filepath.Join(d.dir, "mmlu", "test", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("prompts: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("prompts: no MMLU subjects in %s (run scripts/fetch_eval_data.sh)", d.dir)
	}
	for _, f := range files {
		d.subjs = append(d.subjs, strings.TrimSuffix(filepath.Base(f), ".jsonl"))
	}
	sort.Strings(d.subjs)
	return d.subjs, nil
}

// mmluPrompt is RouteLLM's MMLU prompt text (internal/wire mmluLabelPrompt).
func mmluPrompt(q string, choices []string) string {
	var b strings.Builder
	b.WriteString(q)
	for i, c := range choices {
		fmt.Fprintf(&b, "\n%c. %s", mmluLetters[i], c)
	}
	b.WriteString("\nAnswer:")
	return b.String()
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path) //nolint:gosec // G304: file under the eval data directory
	if err != nil {
		return nil, fmt.Errorf("prompts: eval data (run scripts/fetch_eval_data.sh): %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var rows []T
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r T
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("prompts: %s: %w", path, err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("prompts: %s: %w", path, err)
	}
	return rows, nil
}
