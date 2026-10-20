// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// TRG-06 sends masked requests through a mask route whose only target is a
// cloud fakellm model. Every case scripts fakellm (echo or tool-call, with a
// chunk size of 1-32 bytes) and is checked for byte-exact restoration. The
// generator picks chunk size and leading padding so that, per streamed mode
// (content deltas, tool-call argument deltas), every placeholder type is split
// at every byte offset; the coverage is then measured from the masked bodies
// fakellm actually received, not from the generator's prediction.

const (
	trg06Route    = "trg06-mask"
	trg06Model    = "trg06-strong"
	trg06Cloud    = "cloud-strong"
	trg06MaxChunk = 32
	trg06TagAlpha = "qwxyzjkvfg" // letters only: a case tag never looks like PII
)

// Stream modes. Only the streamed modes split placeholders across chunks.
const (
	trg06TextStream = iota
	trg06ToolStream
	trg06TextJSON
	trg06ToolJSON
	trg06Modes
)

var trg06ModeNames = [trg06Modes]string{"text-stream", "tool-stream", "text-json", "tool-json"}

// trg06Snippet is one PII value with its surrounding text. Neighbours include
// `"` and `\` (JSON escaping in tool arguments) and context words for the
// context-anchored detectors.
type trg06Snippet struct {
	typ, before, value, after string
}

var trg06Catalogue = []trg06Snippet{
	{"email", `reply to "`, "jo.doe@example.com", `" today`},
	{"email", `path C:\users\`, "ana.lee+test@mail.co.uk", `\" end`},
	{"email", "메일 ", "minh.tran@corp.example.vn", " 로 보내"},
	{"credit_card", `card "`, "4111 1111 1111 1111", `" exp 12/29`},
	{"credit_card", `pay\`, "5555-5555-5555-4444", `\ now`},
	{"credit_card", "Amex: ", "378282246310005", "."},
	{"iban", `IBAN: "`, "GB82 WEST 1234 5698 7654 32", `"`},
	{"iban", `acct\`, "DE89370400440532013000", `\n`},
	{"ip", `host="`, "10.20.30.40", `" up`},
	{"ip", `peer \`, "192.168.1.254", `\`},
	{"ip", "addr ", "2001:db8::8a2e:370:7334", " (v6)"},
	{"us_ssn", `ssn "`, "536-22-1234", `"`},
	{"us_ssn", `SSN\`, "219-09-9999", `\ on file`},
	{"secret", `key="`, "sk-Abc123def456GHI789jkl0", `"`},
	{"secret", `aws \`, "AKIAIOSFODNN7EXAMPLE", `\ id`},
	{"secret", "token: ", "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789", " ok"},
	{"kr_rrn", `주민번호 "`, "900101-1234567", `"`},
	{"kr_rrn", `RRN\`, "851231-2345678", `\ 확인`},
	{"phone", `phone: "`, "010-1234-5678", `"`},
	{"phone", `tel\`, "(415) 555-2671", `\ call`},
	{"phone", "전화 ", "010.9876.5432", " 주세요"},
	{"phone", `fax "`, "+44 20 7946 0958", `"`},
	{"vn_cccd", `CCCD "`, "001203004567", `"`},
	{"vn_cccd", "số định danh: ", "079301234567", " nhé"},
}

var (
	trg06JoinWords = []string{" and ", "\n", ", then ", "\t| ", ` \"q\" `}
	trg06AnyPH     = regexp.MustCompile(`\[\[[A-Z_]+_[0-9]+\]\]`)
	trg06TagRE     = regexp.MustCompile(` ref ([` + trg06TagAlpha + `]+)$`)
)

// trg06Case is one request.
type trg06Case struct {
	idx    int
	mode   int
	chunk  int
	text   string
	values []string
	types  []string
}

func (c trg06Case) tool() bool   { return c.mode == trg06ToolStream || c.mode == trg06ToolJSON }
func (c trg06Case) stream() bool { return c.mode == trg06TextStream || c.mode == trg06ToolStream }

// trg06PH is the placeholder for type and index ([[TYPE_n]]).
func trg06PH(typ string, n int) string { return fmt.Sprintf("[[%s_%d]]", strings.ToUpper(typ), n) }

func trg06Tag(i int) string {
	s := fmt.Sprintf("%04d", i)
	b := make([]byte, len(s))
	for k := range s {
		b[k] = trg06TagAlpha[s[k]-'0']
	}
	return string(b)
}

// trg06JSONPrefix is the encoding fakellm applies to the user text inside the
// tool-call arguments, up to (not including) the closing quote.
func trg06JSONPrefix(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSuffix(b.String(), "\n")
	return out[:len(out)-1]
}

// trg06Streamed is the string fakellm splits into deltas for a masked text.
func trg06Streamed(tool bool, masked string) string {
	if tool {
		return `{"input": ` + trg06JSONPrefix(masked) + `"}`
	}
	return masked
}

// trg06Bounds mirrors fakellm's chunking (contract C2: chunks of size bytes,
// UTF-8 boundaries respected) and returns every chunk end offset.
func trg06Bounds(s string, size int) []int {
	var out []int
	for i := 0; i < len(s); {
		end := min(i+size, len(s))
		for end < len(s) && !utf8.RuneStart(s[end]) {
			end++
		}
		out = append(out, end)
		i = end
	}
	return out
}

// trg06Target is a (snippet, split offset) pair the generator aims for.
type trg06Target struct {
	snip, off int
}

// trg06Targets lists, for every detector type, every split offset of its
// first placeholder, rotating through the type's snippets.
func trg06Targets() []trg06Target {
	byType := map[string][]int{}
	var types []string
	for i, s := range trg06Catalogue {
		if _, ok := byType[s.typ]; !ok {
			types = append(types, s.typ)
		}
		byType[s.typ] = append(byType[s.typ], i)
	}
	var out []trg06Target
	for _, typ := range types {
		snips := byType[typ]
		for off := 1; off < len(trg06PH(typ, 1)); off++ {
			out = append(out, trg06Target{snip: snips[off%len(snips)], off: off})
		}
	}
	return out
}

// trg06Split picks a chunk size and padding length such that the first
// placeholder (after padding+before) is split at offset off.
func trg06Split(rng *rand.Rand, tool bool, padCh, before, ph string, off int) (chunk, pad int) {
	c0 := 1 + rng.IntN(trg06MaxChunk)
	for d := range trg06MaxChunk {
		c := (c0-1+d)%trg06MaxChunk + 1
		for p := range c {
			pre := strings.Repeat(padCh, p) + before
			s := pre + ph + "~"
			at := len(pre)
			if tool {
				s = `{"input": ` + trg06JSONPrefix(pre) + ph + "~"
				at = len(s) - len(ph) - 1
			}
			if slices.Contains(trg06Bounds(s, c), at+off) {
				return c, p
			}
		}
	}
	return 1, 0
}

// trg06Cases builds n cases cycling through the four modes; streamed cases
// walk the target list, the others use random snippets and padding.
func trg06Cases(n int) []trg06Case {
	rng := rand.New(rand.NewPCG(20261019, 6)) //nolint:gosec // seeded case generator, not security
	targets := trg06Targets()
	cases := make([]trg06Case, 0, n)
	for i := range n {
		c := trg06Case{idx: i, mode: i % trg06Modes}
		padCh := []string{" ", "~"}[rng.IntN(2)]
		var first trg06Snippet
		pad := 0
		if c.stream() {
			tg := targets[(i/trg06Modes)%len(targets)]
			first = trg06Catalogue[tg.snip]
			c.chunk, pad = trg06Split(rng, c.tool(), padCh, first.before, trg06PH(first.typ, 1), tg.off)
		} else {
			first = trg06Catalogue[rng.IntN(len(trg06Catalogue))]
			c.chunk, pad = 1+rng.IntN(trg06MaxChunk), rng.IntN(trg06MaxChunk)
		}
		var b strings.Builder
		b.WriteString(strings.Repeat(padCh, pad))
		c.add(&b, first)
		for range rng.IntN(3) {
			b.WriteString(trg06JoinWords[rng.IntN(len(trg06JoinWords))])
			switch r := rng.IntN(8); {
			case r == 0: // the same value again: same placeholder
				b.WriteString(first.value)
			case r == 1 && first.typ != "secret": // a literal placeholder-like token in the original
				b.WriteString("[[SECRET_1]]")
			default:
				c.add(&b, trg06Catalogue[rng.IntN(len(trg06Catalogue))])
			}
		}
		b.WriteString(" ref " + trg06Tag(i))
		c.text = b.String()
		cases = append(cases, c)
	}
	return cases
}

func (c *trg06Case) add(b *strings.Builder, s trg06Snippet) {
	b.WriteString(s.before + s.value + s.after)
	c.values = append(c.values, s.value)
	c.types = append(c.types, s.typ)
}

const trg06Config = `
dispatch: { attempt_timeout: 10s, total_timeout: 30s }
routes:
  - name: ` + trg06Route + `
    pool: [cloud-strong]
    privacy: { mode: mask }
    default_tier: strong
`

var trg06Tools = []map[string]any{{
	"type": "function",
	"function": map[string]any{
		"name": "record_input",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"input": map[string]any{"type": "string"}},
		},
	},
}}

// run sends one case and returns the restored text (or a failure reason).
func (c trg06Case) run(tb testing.TB, f *fake, tr *triage) (string, error) {
	tb.Helper()
	scenario := "echo"
	if c.tool() {
		scenario = "tool-call"
	}
	f.script(tb, []map[string]any{{
		"model": trg06Model, "scenario": scenario, "count": 1,
		"params": map[string]any{"chunk_bytes": c.chunk},
	}})
	body := chatBody(trg06Route, c.text, c.stream())
	if c.tool() {
		body["tools"] = trg06Tools
	}
	r, err := tr.chat(context.Background(), body)
	if err != nil {
		return "", err
	}
	if r.status != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %.200s", r.status, r.body)
	}
	if t := r.header.Get("X-Triage-Target"); t != trg06Cloud {
		return "", fmt.Errorf("target %q, want %s", t, trg06Cloud)
	}
	return c.extract(r.body)
}

// extract returns the restored user text: the content, or the tool-call
// arguments' "input" after JSON parsing.
func (c trg06Case) extract(body []byte) (string, error) {
	output := trg06JSONOutput
	if c.stream() {
		output = trg06StreamOutput
	}
	text, args, err := output(body)
	if err != nil || !c.tool() {
		return text, err
	}
	var in struct {
		Input *string `json:"input"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("arguments %q: %w", args, err)
	}
	if in.Input == nil {
		return "", fmt.Errorf("arguments %q have no string input", args)
	}
	return *in.Input, nil
}

func trg06StreamOutput(body []byte) (text, args string, err error) {
	s, err := parseStream(body)
	if err != nil {
		return "", "", err
	}
	if !s.done || s.errorEvent {
		return "", "", fmt.Errorf("stream done=%v error=%v", s.done, s.errorEvent)
	}
	return s.text, s.args, nil
}

func trg06JSONOutput(body []byte) (text, args string, err error) {
	var resp chatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", fmt.Errorf("decode response: %w", err)
	}
	if len(resp.Choices) != 1 {
		return "", "", fmt.Errorf("%d choices", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) == 1 {
		args = msg.ToolCalls[0].Function.Arguments
	}
	return msg.Content, args, nil
}

// trg06Upstream is what the fakellm log shows for the cases.
type trg06Upstream struct {
	unmatched, leaked, missingPH int
	// covered[mode][L][off] is true once a placeholder of length L was split at off.
	covered [trg06Modes]map[int][]bool
	issues  []string
}

func (u *trg06Upstream) issue(format string, a ...any) {
	if len(u.issues) < 10 {
		u.issues = append(u.issues, fmt.Sprintf(format, a...))
	}
}

// checkUpstream matches every fakellm record to its case by tag, checks that
// it carries placeholders instead of values and records the split offsets.
func checkUpstream(recs []fakeRecord, cases []trg06Case) trg06Upstream {
	var u trg06Upstream
	for m := range u.covered {
		u.covered[m] = map[int][]bool{}
	}
	seen := make([]int, len(cases))
	for _, rec := range recs {
		if rec.Model != trg06Model {
			continue
		}
		masked, ok := trg06LastUser(rec.Body)
		m := trg06TagRE.FindStringSubmatch(masked)
		if !ok || m == nil {
			u.unmatched++
			u.issue("upstream body without a case tag: %.120s", rec.Body)
			continue
		}
		i := trg06Untag(m[1])
		if i < 0 || i >= len(cases) {
			u.unmatched++
			continue
		}
		seen[i]++
		u.checkCase(cases[i], rec.Body, masked)
	}
	for i, n := range seen {
		if n != 1 {
			u.unmatched++
			u.issue("case %d reached fakellm %d times", i, n)
		}
	}
	return u
}

func (u *trg06Upstream) checkCase(c trg06Case, body, masked string) {
	for _, v := range c.values {
		if strings.Contains(body, v) {
			u.leaked++
			u.issue("case %d: value %q reached the cloud target", c.idx, v)
		}
	}
	for _, typ := range c.types {
		re := regexp.MustCompile(`\[\[` + strings.ToUpper(typ) + `_[0-9]+\]\]`)
		if !re.MatchString(masked) {
			u.missingPH++
			u.issue("case %d: no %s placeholder in %q", c.idx, typ, masked)
		}
	}
	if !c.stream() {
		return
	}
	s := trg06Streamed(c.tool(), masked)
	bounds := trg06Bounds(s, c.chunk)
	for _, loc := range trg06AnyPH.FindAllStringIndex(s, -1) {
		if strings.Contains(c.text, s[loc[0]:loc[1]]) {
			continue // a literal token of the original, not a placeholder
		}
		l := loc[1] - loc[0]
		cov := u.covered[c.mode][l]
		if cov == nil {
			cov = make([]bool, l)
			u.covered[c.mode][l] = cov
		}
		for _, b := range bounds {
			if b > loc[0] && b < loc[1] {
				cov[b-loc[0]] = true
			}
		}
	}
}

// missingSplits counts uncovered (mode, length, offset) triples.
func (u *trg06Upstream) missingSplits() (missing int, lengths map[string][]int) {
	lengths = map[string][]int{}
	for _, m := range []int{trg06TextStream, trg06ToolStream} {
		for l, cov := range u.covered[m] {
			lengths[trg06ModeNames[m]] = append(lengths[trg06ModeNames[m]], l)
			for off := 1; off < l; off++ {
				if !cov[off] {
					missing++
					u.issue("%s: placeholder length %d never split at offset %d", trg06ModeNames[m], l, off)
				}
			}
		}
		sort.Ints(lengths[trg06ModeNames[m]])
	}
	return missing, lengths
}

func trg06LastUser(body string) (string, bool) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(body), &req) != nil || len(req.Messages) == 0 {
		return "", false
	}
	last := req.Messages[len(req.Messages)-1]
	return last.Content, last.Role == "user"
}

func trg06Untag(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + strings.IndexRune(trg06TagAlpha, r)
	}
	return n
}

// TestAcceptance_TRG06_MaskRoundTrip implements TRG-06.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG06_MaskRoundTrip(t *testing.T) {
	th := thresholds(t, "TRG-06")
	casesMin, toolMin := int(num(t, th, "cases_min")), int(num(t, th, "tool_call_cases_min"))
	f := composeFake(t)
	tr := startTriage(t, targetsYAML(f, "trg06", "{}", "{}")+trg06Config)

	cases := trg06Cases(max(casesMin, 2*toolMin))
	restored, tool, streamed := 0, 0, 0
	var failures []string
	for _, c := range cases {
		if c.tool() {
			tool++
		}
		if c.stream() {
			streamed++
		}
		got, err := c.run(t, f, tr)
		switch {
		case err != nil:
			failures = append(failures, fmt.Sprintf("case %d (%s): %v", c.idx, trg06ModeNames[c.mode], err))
		case got != c.text:
			failures = append(failures, fmt.Sprintf("case %d (%s, chunk %d): restored %q, want %q",
				c.idx, trg06ModeNames[c.mode], c.chunk, got, c.text))
		default:
			restored++
		}
	}

	recs, dropped := f.requests(t)
	up := checkUpstream(recs, cases)
	missing, lengths := up.missingSplits()
	ratio := float64(restored) / float64(len(cases))
	pass := ratio >= num(t, th, "restored_ratio") && len(cases) >= casesMin && tool >= toolMin &&
		float64(dropped) <= num(t, th, "dropped") && up.unmatched == 0 && up.leaked == 0 &&
		up.missingPH == 0 && missing == 0
	failures = append(failures, up.issues...)
	details := fmt.Sprintf("%d/%d restored byte for byte (%d tool-call, %d streamed); split coverage gaps %d",
		restored, len(cases), tool, streamed, missing)
	if len(failures) > 0 {
		details += "; first failures: " + strings.Join(failures[:min(len(failures), 5)], " | ")
	}
	writeResult(t, Result{ID: "TRG-06", Pass: pass, Details: details, Measured: map[string]any{
		"cases": len(cases), "tool_call_cases": tool, "streamed_cases": streamed,
		"restored": restored, "restored_ratio": ratio, "dropped": dropped,
		"upstream_unmatched": up.unmatched, "values_leaked": up.leaked, "placeholders_missing": up.missingPH,
		"split_offsets_missing": missing, "split_placeholder_lengths": lengths,
	}})
	require.True(t, pass, details)
}
