// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/thnhdatle/triage/internal/app/eval"
)

// Item id grammar of the benchmark sets (eval/README.md):
//
//	gsm8k/test:<row>             zero-based row of the GSM8K test split
//	gsm8k/test:*                 every GSM8K test row not in RouteLLM's contaminated prompts
//	mmlu/test:<subject>:<row>    zero-based row in the test split of one MMLU subject
//
// Ids refer to the converted data written by scripts/fetch_eval_data.sh.
const (
	srcGSM8K = "gsm8k/test"
	srcMMLU  = "mmlu/test"
	// idWildcard in place of a gsm8k row selects all rows.
	idWildcard = "*"
)

var subjectRE = regexp.MustCompile(`^[a-z0-9_]+$`)

// readItemIDs reads an id list: one id per line, blank lines and lines
// starting with # ignored, duplicates dropped keeping the first.
func readItemIDs(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // operator-chosen set file
	if err != nil {
		return nil, fmt.Errorf("item set: %w", err)
	}
	defer func() { _ = f.Close() }()
	var ids []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		if id == "" || strings.HasPrefix(id, "#") || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("item set %s: %w", path, err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("item set %s: no ids", path)
	}
	return ids, nil
}

// itemID is a parsed benchmark item id.
type itemID struct {
	source  string // srcGSM8K or srcMMLU
	subject string // MMLU only
	row     int    // -1 for the wildcard
}

func parseItemID(id string) (itemID, error) {
	src, rest, ok := strings.Cut(id, ":")
	if !ok {
		return itemID{}, fmt.Errorf("item id %q: want gsm8k/test:<row> or mmlu/test:<subject>:<row>", id)
	}
	out := itemID{source: src}
	rowText := rest
	switch src {
	case srcGSM8K:
		if rest == idWildcard {
			out.row = -1
			return out, nil
		}
	case srcMMLU:
		sub, r, ok := strings.Cut(rest, ":")
		if !ok || !subjectRE.MatchString(sub) {
			return itemID{}, fmt.Errorf("item id %q: want mmlu/test:<subject>:<row>", id)
		}
		out.subject, rowText = sub, r
	default:
		return itemID{}, fmt.Errorf("item id %q: unknown source %q (gsm8k/test, mmlu/test)", id, src)
	}
	n, err := strconv.Atoi(rowText)
	if err != nil || n < 0 {
		return itemID{}, fmt.Errorf("item id %q: row must be a non-negative integer", id)
	}
	out.row = n
	return out, nil
}

type gsm8kRow struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type mmluRow struct {
	Subject  string   `json:"subject"`
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
	Answer   int      `json:"answer"`
}

// mmluLetters label the choices.
const mmluLetters = "ABCD"

// mmluLabelPrompt is the prompt text of RouteLLM's MMLU label files.
func mmluLabelPrompt(r mmluRow) string {
	var b strings.Builder
	b.WriteString(r.Question)
	for i, c := range r.Choices {
		fmt.Fprintf(&b, "\n%c. %s", mmluLetters[i], c)
	}
	b.WriteString("\nAnswer:")
	return b.String()
}

type labelRow struct{ weak, strong bool }

// evalData resolves benchmark ids against the converted data directory and,
// in labels mode, the RouteLLM label files.
type evalData struct {
	dir       string
	promptDir string
	weakCol   string
	strongCol string
	live      bool

	gsm      []gsm8kRow
	mmlu     map[string][]mmluRow
	labels   map[string]map[string]labelRow // label file -> prompt -> labels
	tmpl     map[string]*template.Template
	excluded map[string]bool // contaminated GSM8K questions, loaded on demand for the wildcard
}

func newEvalData(dir, promptDir, weakCol, strongCol string, live bool) *evalData {
	return &evalData{
		dir: dir, promptDir: promptDir, weakCol: weakCol, strongCol: strongCol, live: live,
		mmlu: map[string][]mmluRow{}, labels: map[string]map[string]labelRow{}, tmpl: map[string]*template.Template{},
	}
}

// items resolves ids into dataset items, expanding the gsm8k wildcard.
func (d *evalData) items(ids []string) ([]datasetItem, error) {
	var out []datasetItem
	seen := map[string]bool{}
	add := func(it datasetItem) {
		if !seen[it.ID] {
			seen[it.ID] = true
			out = append(out, it)
		}
	}
	for _, id := range ids {
		p, err := parseItemID(id)
		if err != nil {
			return nil, err
		}
		if p.source == srcGSM8K && p.row < 0 {
			all, err := d.allGSM8K()
			if err != nil {
				return nil, err
			}
			for _, it := range all {
				add(it)
			}
			continue
		}
		it, err := d.item(p, id)
		if err != nil {
			return nil, err
		}
		add(it)
	}
	return out, nil
}

// allGSM8K returns every GSM8K test item except the contaminated prompts.
func (d *evalData) allGSM8K() ([]datasetItem, error) {
	rows, err := d.gsm8k()
	if err != nil {
		return nil, err
	}
	bad, err := d.contaminatedGSM8K()
	if err != nil {
		return nil, err
	}
	var out []datasetItem
	for i, r := range rows {
		if bad[r.Question] {
			continue
		}
		it, err := d.item(itemID{source: srcGSM8K, row: i}, srcGSM8K+":"+strconv.Itoa(i))
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func (d *evalData) item(p itemID, id string) (datasetItem, error) {
	switch p.source {
	case srcGSM8K:
		rows, err := d.gsm8k()
		if err != nil {
			return datasetItem{}, err
		}
		if p.row >= len(rows) {
			return datasetItem{}, fmt.Errorf("item %s: only %d rows", id, len(rows))
		}
		r := rows[p.row]
		it := datasetItem{ID: id, Reference: r.Answer, Scorer: eval.ScorerGSM8K}
		return d.fill(it, "gsm8k", r, r.Question, filepath.Join("routellm", "gsm8k", "gsm8k_responses.csv"))
	default:
		rows, err := d.mmluSubject(p.subject)
		if err != nil {
			return datasetItem{}, err
		}
		if p.row >= len(rows) {
			return datasetItem{}, fmt.Errorf("item %s: subject has only %d rows", id, len(rows))
		}
		r := rows[p.row]
		if r.Answer < 0 || r.Answer >= len(r.Choices) || len(r.Choices) > len(mmluLetters) {
			return datasetItem{}, fmt.Errorf("item %s: malformed MMLU row", id)
		}
		it := datasetItem{ID: id, Reference: string(mmluLetters[r.Answer]), Scorer: eval.ScorerMMLU}
		return d.fill(it, "mmlu", r, mmluLabelPrompt(r),
			filepath.Join("routellm", "mmlu", "responses", "mmlu_"+p.subject+".csv"))
	}
}

// fill sets the prompt: rendered from the template in live mode, the prompt
// text of the label file (with its labels) in labels mode.
func (d *evalData) fill(it datasetItem, tmplName string, row any, labelPrompt, labelFile string) (datasetItem, error) {
	if d.live {
		text, err := d.render(tmplName, row)
		if err != nil {
			return it, fmt.Errorf("item %s: %w", it.ID, err)
		}
		it.Prompt = text
		return it, nil
	}
	rows, err := d.labelFile(labelFile)
	if err != nil {
		return it, err
	}
	l, ok := rows[labelPrompt]
	if !ok {
		return it, fmt.Errorf("item %s: prompt not in %s", it.ID, labelFile)
	}
	it.Prompt, it.WeakCorrect, it.StrongCorrect = labelPrompt, &l.weak, &l.strong
	return it, nil
}

type tmplChoice struct{ Letter, Text string }

func (d *evalData) render(name string, row any) (string, error) {
	t, ok := d.tmpl[name]
	if !ok {
		if d.promptDir == "" {
			return "", errors.New("dataset.prompts is required in live mode")
		}
		path := filepath.Join(d.promptDir, name+".txt")
		raw, err := os.ReadFile(path) //nolint:gosec // operator-chosen prompt directory
		if err != nil {
			return "", fmt.Errorf("prompt template: %w", err)
		}
		if t, err = template.New(name).Option("missingkey=error").Parse(string(raw)); err != nil {
			return "", fmt.Errorf("prompt template %s: %w", path, err)
		}
		d.tmpl[name] = t
	}
	data := map[string]any{}
	switch r := row.(type) {
	case gsm8kRow:
		data["Question"] = r.Question
	case mmluRow:
		data["Question"], data["Subject"] = r.Question, strings.ReplaceAll(r.Subject, "_", " ")
		cs := make([]tmplChoice, len(r.Choices))
		for i, c := range r.Choices {
			cs[i] = tmplChoice{Letter: string(mmluLetters[i]), Text: c}
		}
		data["Choices"] = cs
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("prompt template %s: %w", name, err)
	}
	return b.String(), nil
}

func (d *evalData) gsm8k() ([]gsm8kRow, error) {
	if d.gsm == nil {
		rows, err := readJSONLRows[gsm8kRow](filepath.Join(d.dir, "gsm8k", "test.jsonl"))
		if err != nil {
			return nil, err
		}
		d.gsm = rows
	}
	return d.gsm, nil
}

func (d *evalData) mmluSubject(subject string) ([]mmluRow, error) {
	if rows, ok := d.mmlu[subject]; ok {
		return rows, nil
	}
	rows, err := readJSONLRows[mmluRow](filepath.Join(d.dir, "mmlu", "test", subject+".jsonl"))
	if err != nil {
		return nil, err
	}
	d.mmlu[subject] = rows
	return rows, nil
}

func (d *evalData) contaminatedGSM8K() (map[string]bool, error) {
	if d.excluded != nil {
		return d.excluded, nil
	}
	rows, err := readJSONLRows[struct {
		EvalPrompt string `json:"eval_prompt"`
	}](filepath.Join(d.dir, "routellm", "gsm8k", "contaminated_prompts.jsonl"))
	if err != nil {
		return nil, err
	}
	d.excluded = make(map[string]bool, len(rows))
	for _, r := range rows {
		d.excluded[r.EvalPrompt] = true
	}
	return d.excluded, nil
}

func readJSONLRows[T any](path string) ([]T, error) {
	f, err := os.Open(path) //nolint:gosec // path under the eval data directory
	if err != nil {
		return nil, fmt.Errorf("eval data (run scripts/fetch_eval_data.sh): %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxDatasetLine)
	var rows []T
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r T
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("eval data %s: %w", path, err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("eval data %s: %w", path, err)
	}
	return rows, nil
}

// labelFile reads one RouteLLM label CSV into prompt -> correctness of the
// configured weak and strong columns. The first row of a repeated prompt wins.
func (d *evalData) labelFile(rel string) (map[string]labelRow, error) {
	if m, ok := d.labels[rel]; ok {
		return m, nil
	}
	if d.weakCol == "" || d.strongCol == "" {
		return nil, errors.New("dataset.labels.weak and dataset.labels.strong are required in labels mode")
	}
	f, err := os.Open(filepath.Join(d.dir, rel)) //nolint:gosec // path under the eval data directory
	if err != nil {
		return nil, fmt.Errorf("eval labels (run scripts/fetch_eval_data.sh): %w", err)
	}
	defer func() { _ = f.Close() }()
	r := csv.NewReader(f)
	head, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("eval labels %s: %w", rel, err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	pi, wi, si := col["prompt"], -1, -1
	for _, c := range []struct {
		name string
		dst  *int
	}{{d.weakCol, &wi}, {d.strongCol, &si}} {
		i, ok := col[c.name]
		if !ok {
			return nil, fmt.Errorf("eval labels %s: no column %q", rel, c.name)
		}
		*c.dst = i
	}
	m := map[string]labelRow{}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("eval labels %s: %w", rel, err)
		}
		w, werr := strconv.ParseBool(rec[wi])
		s, serr := strconv.ParseBool(rec[si])
		if werr != nil || serr != nil {
			return nil, fmt.Errorf("eval labels %s: non-boolean label", rel)
		}
		if _, dup := m[rec[pi]]; !dup {
			m[rec[pi]] = labelRow{weak: w, strong: s}
		}
	}
	d.labels[rel] = m
	return m, nil
}
