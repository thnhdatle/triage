// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/dispatch"
)

// TRG-01 sends every labelled sample of testdata/pii/labelled.jsonl whose
// types belong to the checksum, structural and context-anchored detectors
// (street_address, a heuristic disabled by default, is excluded) through
// local_only and mask routes, streamed and non-streamed, with the PII in the
// user message, in assistant history or in tool-call arguments and tool
// results. More than 100 cases script a mid-stream cut on a local target that
// echoes the user text, so the relayed partial output holds every PII value,
// and the continuation goes to a cloud target under mask (or, on a local_only
// route, to another local target). Every body fakellm received for a cloud
// model is then searched byte for byte for every PII value of the run.

const (
	trg01ID             = "TRG-01"
	trg01Prefix         = "trg01"
	trg01Labelled       = "../testdata/pii/labelled.jsonl"
	trg01Seed           = 20261019
	trg01Workers        = 16
	trg01MinCont        = 100
	trg01MinFormats     = 10
	trg01Heuristic      = "street_address"
	trg01Filler         = "\n\n(End of message. Please keep this reference paragraph intact for the audit trail.)"
	trg01MaxFailures    = 20
	trg01PlaceUser      = "user"
	trg01PlaceHistory   = "history"
	trg01PlaceTool      = "tool"
	trg01ModeLocal      = "local_only"
	trg01ModeMask       = "mask"
	trg01VariantMaskCut = "mask-cut-continuation/sse"
)

// trg01Types are the detector types the criterion allows (checksum,
// structural and context-anchored classes).
var trg01Types = []string{
	"credit_card", "iban", "email", "ip", "us_ssn", "secret", "kr_rrn", "phone", "vn_cccd",
}

// trg01CloudModels are the upstream model names of the cloud targets.
var trg01CloudModels = map[string]bool{trg01Prefix + "-cloud": true, trg01Prefix + "-tools": true}

type trg01Sample struct {
	Text  string `json:"text"`
	Lang  string `json:"lang"`
	Spans []struct {
		Start int    `json:"start"`
		End   int    `json:"end"`
		Type  string `json:"type"`
	} `json:"spans"`
}

// trg01Variant is one route/transport combination.
type trg01Variant struct {
	name       string
	route      string
	stream     bool
	tools      bool
	cut        bool
	clientMode string // X-Triage-Privacy request header
	mode       string // expected effective privacy mode
	local      bool   // every attempt must be local
	contLocal  bool   // locality of the continuation attempt (cut cases)
}

// trg01Variants is weighted: mask continuation appears twice.
var trg01Variants = []trg01Variant{
	{name: "local_only/json", route: "pii-local-only", mode: trg01ModeLocal, local: true},
	{name: "local_only/sse", route: "pii-local-only", stream: true, mode: trg01ModeLocal, local: true},
	{name: "mask-cloud/json", route: "pii-mask-cloud", mode: trg01ModeMask},
	{name: "mask-cloud/sse", route: "pii-mask-cloud", stream: true, mode: trg01ModeMask},
	{name: "mask-tools/json", route: "pii-mask-tools", tools: true, mode: trg01ModeMask},
	{name: "mask-tools/sse", route: "pii-mask-tools", tools: true, stream: true, mode: trg01ModeMask},
	{name: "mask-local/sse", route: "pii-mask-local", stream: true, mode: trg01ModeMask, local: true},
	{
		name: "mask+client-local_only/json", route: "pii-mask-cloud", clientMode: trg01ModeLocal,
		mode: trg01ModeLocal, local: true,
	},
	{name: trg01VariantMaskCut, route: "pii-mask-cut", stream: true, cut: true, mode: trg01ModeMask},
	{name: trg01VariantMaskCut, route: "pii-mask-cut", stream: true, cut: true, mode: trg01ModeMask},
	{
		name: "local_only-cut-continuation/sse", route: "pii-local-only-cut", stream: true, cut: true,
		mode: trg01ModeLocal, local: true, contLocal: true,
	},
}

// trg01Case is one request.
type trg01Case struct {
	id         int
	v          trg01Variant
	placement  string
	lang       string
	messages   []map[string]any
	lastUser   string
	values     []string
	valueTypes []string // detector type of values[i]
	types      map[string]int
	formats    []string
	chunkBytes int
	cutAfter   int
	answer     string // fakellm answer template of the cut attempt
	newValues  int    // PII values in the cut output that the request lacks
	partial    string
}

// trg01Stats accumulates the run.
type trg01Stats struct {
	mu        sync.Mutex
	failures  []string
	nFailures int
	failedBy  map[string]int
	records   []fakeRecord
	dropped   int
}

func (s *trg01Stats) fail(c *trg01Case, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nFailures++
	s.failedBy[c.v.name]++
	if len(s.failures) < trg01MaxFailures {
		s.failures = append(s.failures, fmt.Sprintf("case %d (%s, %s, %s): %v", c.id, c.v.name, c.placement, c.lang, err))
	}
}

// TestAcceptance_TRG01_PIINeverLeaves implements TRG-01.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG01_PIINeverLeaves(t *testing.T) {
	th := thresholds(t, trg01ID)
	f := composeFake(t)
	tr := startTriage(t, trg01Config(f))
	cases := trg01Cases(t)
	st := &trg01Stats{failedBy: map[string]int{}}

	var plain, cut []*trg01Case
	for _, c := range cases {
		if c.v.cut {
			cut = append(cut, c)
		} else {
			plain = append(plain, c)
		}
	}
	trg01RunConcurrent(tr, plain, st)
	trg01Harvest(t, f, st)
	trg01RunCut(t, f, tr, cut, st)
	trg01Harvest(t, f, st)

	measured, pass := trg01Evaluate(t, th, cases, st)
	details := "every PII value absent from every body received by a cloud target; all responses succeeded"
	if !pass {
		details = fmt.Sprintf("failures=%d first=%q", st.nFailures, st.failures)
	}
	writeResult(t, Result{ID: trg01ID, Measured: measured, Pass: pass, Details: details})
	require.True(t, pass, "TRG-01 failed: %v; failures: %s", measured, strings.Join(st.failures, "\n"))
}

func trg01Config(f *fake) string {
	p := trg01Prefix
	return `
server: { listen: "127.0.0.1:0" }
admin: { listen: "127.0.0.1:0" }
auth: { enabled: false }
api: { expose_decision_detail: true, expose_targets: true }
providers:
  fake: { type: openaicompat, base_url: "` + f.baseURL() + `", max_idle_conns_per_host: 256 }
# Every cut counts as a failure of local-cut; passive health must not take
# it out of rotation, or the cut cases would route straight to the cloud.
health: { interval: 1h, failure_threshold: 1000000 }
decisions: { buffer_size: 20000 }
dispatch: { attempt_timeout: 10s, total_timeout: 30s, backoff: { base: 1ms, max: 5ms } }
targets:
  - { name: local-echo, provider: fake, model: ` + p + `-local, locality: local, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: echo } }
  - { name: local-cut, provider: fake, model: ` + p + `-cut, locality: local, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode] }
  - { name: local-backup, provider: fake, model: ` + p + `-backup, locality: local, tier: strong,
      context_window: 32768, capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: echo } }
  - { name: cloud-strong, provider: fake, model: ` + p + `-cloud, locality: cloud, tier: strong,
      context_window: 200000, capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: echo } }
  - { name: cloud-tools, provider: fake, model: ` + p + `-tools, locality: cloud, tier: strong,
      context_window: 200000, capabilities: [tools, json_mode], extra_headers: { X-Fake-Scenario: tool-call } }
routes:
  - { name: pii-local-only, pool: [local-echo, cloud-strong], privacy: { mode: local_only },
      default_tier: strong, on_tier_unavailable: degrade, fallback: [tier:strong, tier:weak] }
  - { name: pii-local-only-cut, pool: [local-cut, local-backup, cloud-strong], privacy: { mode: local_only },
      default_tier: weak, fallback: [tier:strong] }
  - { name: pii-mask-cloud, pool: [local-echo, cloud-strong], privacy: { mode: mask },
      default_tier: strong, on_tier_unavailable: degrade }
  - { name: pii-mask-tools, pool: [cloud-tools], privacy: { mode: mask }, default_tier: strong }
  - { name: pii-mask-local, pool: [local-echo, cloud-strong], privacy: { mode: mask }, default_tier: weak }
  - { name: pii-mask-cut, pool: [local-cut, cloud-strong], privacy: { mode: mask },
      default_tier: weak, fallback: [tier:strong] }
`
}

// trg01Cases loads the labelled samples and assigns each a variant and a
// message placement, deterministically.
func trg01Cases(tb testing.TB) []*trg01Case {
	tb.Helper()
	samples := trg01Load(tb)
	rng := rand.New(rand.NewSource(trg01Seed)) //nolint:gosec // G404: deterministic test plan
	rng.Shuffle(len(samples), func(i, j int) { samples[i], samples[j] = samples[j], samples[i] })
	cases := make([]*trg01Case, 0, len(samples))
	for i, s := range samples {
		c := &trg01Case{id: i, v: trg01Variants[i%len(trg01Variants)], lang: s.Lang, types: map[string]int{}}
		c.placement = []string{trg01PlaceUser, trg01PlaceHistory, trg01PlaceTool}[(i/len(trg01Variants))%3]
		maxEnd := 0
		for _, sp := range s.Spans {
			v := s.Text[sp.Start:sp.End]
			c.values = append(c.values, v)
			c.valueTypes = append(c.valueTypes, sp.Type)
			c.types[sp.Type]++
			c.formats = append(c.formats, trg01Format(sp.Type, v, s.Lang))
			maxEnd = max(maxEnd, sp.End)
		}
		if c.v.cut {
			trg01CutCase(tb, c, s, samples[(i+len(samples)/2)%len(samples)], maxEnd)
		} else {
			c.lastUser = s.Text
		}
		c.messages = trg01Messages(c.placement, c.lastUser)
		cases = append(cases, c)
	}
	return cases
}

func trg01Load(tb testing.TB) []trg01Sample {
	tb.Helper()
	fh, err := os.Open(trg01Labelled)
	require.NoError(tb, err)
	defer func() { _ = fh.Close() }()
	var out []trg01Sample
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var s trg01Sample
		require.NoError(tb, json.Unmarshal(sc.Bytes(), &s))
		heuristic := false
		for _, sp := range s.Spans {
			heuristic = heuristic || sp.Type == trg01Heuristic
		}
		if !heuristic && len(s.Spans) > 0 {
			out = append(out, s)
		}
	}
	require.NoError(tb, sc.Err())
	return out
}

// trg01CutCase prepares a cut case. The local target echoes the user text;
// in every other case its output also carries a second sample with PII
// values absent from the request, which only the continuation re-scan
// can find and mask.
func trg01CutCase(tb testing.TB, c *trg01Case, s, extra trg01Sample, maxEnd int) {
	tb.Helper()
	c.placement = trg01PlaceUser
	c.lastUser = s.Text + trg01Filler
	c.answer = "{user}"
	text := c.lastUser
	if c.id%2 == 1 {
		off := len(s.Text) + 1
		for _, sp := range extra.Spans {
			c.values = append(c.values, extra.Text[sp.Start:sp.End])
			c.valueTypes = append(c.valueTypes, sp.Type)
			c.newValues++
			maxEnd = max(maxEnd, off+sp.End)
		}
		text = s.Text + "\n" + extra.Text + trg01Filler
		c.answer = text
	}
	trg01PlanCut(tb, c, text, maxEnd)
}

// trg01PlanCut scripts the local target to echo text in chunks of 8-32
// bytes and cut the stream after the first chunk boundary at or past the
// end of the last PII value (sometimes one chunk later), before the end.
func trg01PlanCut(tb testing.TB, c *trg01Case, text string, maxEnd int) {
	tb.Helper()
	c.chunkBytes = []int{8, 16, 24, 32}[c.id%4]
	pieces := trg01Chunks(text, c.chunkBytes)
	n := 0
	for covered := 0; covered < maxEnd; n++ {
		covered += len(pieces[n])
	}
	if c.id%3 == 0 && n+1 < len(pieces) {
		n++
	}
	require.Less(tb, n, len(pieces), "case %d: the cut must precede the end of the echoed text", c.id)
	c.cutAfter = n
	c.partial = strings.Join(pieces[:n], "")
	for _, v := range c.values {
		require.Contains(tb, c.partial, v, "case %d: the partial output must hold every PII value", c.id)
	}
}

// trg01Chunks mirrors fakellm's chunking (contract C2: UTF-8 boundaries respected).
func trg01Chunks(s string, size int) []string {
	var out []string
	for i := 0; i < len(s); {
		end := min(i+size, len(s))
		for end < len(s) && !utf8.RuneStart(s[end]) {
			end++
		}
		out = append(out, s[i:end])
		i = end
	}
	return out
}

func trg01Messages(placement, text string) []map[string]any {
	switch placement {
	case trg01PlaceHistory:
		return []map[string]any{
			{"role": "user", "content": "Here are my details: " + text},
			{"role": "assistant", "content": "Noted: " + text},
			{"role": "user", "content": text},
		}
	case trg01PlaceTool:
		args, _ := json.Marshal(map[string]string{"query": text})
		return []map[string]any{
			{"role": "user", "content": "Look up my record."},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": "lookup", "arguments": string(args)},
			}}},
			{"role": "tool", "tool_call_id": "call_1", "content": text},
			{"role": "user", "content": text},
		}
	}
	return []map[string]any{{"role": "user", "content": text}}
}

// trg01Format names the value's format variant (detector type plus sub-format).
func trg01Format(typ, v, lang string) string {
	switch typ {
	case "ip":
		if strings.Contains(v, ":") {
			return "ip/v6"
		}
		return "ip/v4"
	case "phone":
		if strings.HasPrefix(v, "+") {
			return "phone/e164"
		}
		return "phone/" + lang
	case "kr_rrn":
		switch {
		case strings.Contains(v, "-"):
			return "kr_rrn/dashed"
		case strings.Contains(v, " "):
			return "kr_rrn/spaced"
		}
		return "kr_rrn/undashed"
	case "secret":
		for _, p := range []string{"sk-", "AKIA", "gh", "xox", "eyJ"} {
			if strings.HasPrefix(v, p) {
				return "secret/" + strings.TrimSuffix(p, "-")
			}
		}
	}
	return typ
}

func trg01Body(c *trg01Case) map[string]any {
	b := map[string]any{"model": c.v.route, "stream": c.v.stream, "messages": c.messages}
	if c.v.tools {
		b["tools"] = []map[string]any{{"type": "function", "function": map[string]any{
			"name": "lookup", "description": "Look up a customer record",
			"parameters": map[string]any{
				"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}},
			},
		}}}
	}
	return b
}

func trg01RunConcurrent(tr *triage, cases []*trg01Case, st *trg01Stats) {
	work := make(chan *trg01Case)
	var wg sync.WaitGroup
	for range trg01Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if err := trg01Run(context.Background(), tr, c); err != nil {
					st.fail(c, err)
				}
			}
		}()
	}
	for _, c := range cases {
		work <- c
	}
	close(work)
	wg.Wait()
}

// trg01RunCut runs the cut cases one at a time, each with its own
// one-request script on the local cut model.
func trg01RunCut(tb testing.TB, f *fake, tr *triage, cases []*trg01Case, st *trg01Stats) {
	tb.Helper()
	for _, c := range cases {
		f.script(tb, []map[string]any{{
			"model": trg01Prefix + "-cut", "scenario": "cut-stream", "count": 1,
			"params": map[string]any{"cut_after": c.cutAfter, "answer": c.answer, "chunk_bytes": c.chunkBytes},
		}})
		if err := trg01Run(context.Background(), tr, c); err != nil {
			st.fail(c, err)
		}
	}
	f.do(tb, http.MethodDelete, "/_fake/script", nil, nil)
}

// trg01Harvest reads the fakellm request log, then clears it.
func trg01Harvest(tb testing.TB, f *fake, st *trg01Stats) {
	tb.Helper()
	recs, dropped := f.requests(tb)
	st.records = append(st.records, recs...)
	st.dropped += dropped
	f.do(tb, http.MethodDelete, "/_fake/requests", nil, nil)
}

func trg01Run(ctx context.Context, tr *triage, c *trg01Case) error {
	var hdr []string
	if c.v.clientMode != "" {
		hdr = []string{"X-Triage-Privacy", c.v.clientMode}
	}
	r, err := tr.chat(ctx, trg01Body(c), hdr...)
	if err != nil {
		return err
	}
	if r.status != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", r.status, r.body)
	}
	if u := r.header.Get("X-Triage-Unmask"); u == "partial" {
		return errors.New("X-Triage-Unmask: partial")
	}
	if err := trg01CheckOutput(c, r.body); err != nil {
		return err
	}
	d, _, err := tr.decision(ctx, r.header.Get("X-Triage-Decision"))
	if err != nil {
		return err
	}
	return trg01CheckDecision(c, d)
}

// trg01CheckOutput verifies the client received the restored text: echo
// targets return the last user text, the tool-call target returns it as
// the "input" argument, cut cases return the partial text followed by the
// continuation target's echo of the continuation instruction.
func trg01CheckOutput(c *trg01Case, body []byte) error {
	text, args, err := trg01Output(c, body)
	if err != nil {
		return err
	}
	want := c.lastUser
	if c.v.cut {
		want = c.partial + dispatch.ContinuationInstruction
	}
	if c.v.tools {
		var a struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return fmt.Errorf("tool arguments %q: %w", args, err)
		}
		text = a.Input
	}
	if text != want {
		return fmt.Errorf("output %q, want %q", text, want)
	}
	return nil
}

func trg01Output(c *trg01Case, body []byte) (text, args string, err error) {
	if !c.v.stream {
		var cr chatResponse
		if err := json.Unmarshal(body, &cr); err != nil {
			return "", "", fmt.Errorf("decode response: %w", err)
		}
		if len(cr.Choices) != 1 {
			return "", "", fmt.Errorf("choices: %s", body)
		}
		m := cr.Choices[0].Message
		if len(m.ToolCalls) > 0 {
			args = m.ToolCalls[0].Function.Arguments
		}
		return m.Content, args, nil
	}
	sr, err := parseStream(body)
	if err != nil {
		return "", "", err
	}
	if !sr.done || sr.errorEvent {
		return "", "", fmt.Errorf("stream incomplete (done=%v error=%v): %s", sr.done, sr.errorEvent, body)
	}
	for _, cm := range sr.comments {
		if strings.Contains(cm, "triage-unmask=partial") {
			return "", "", errors.New("stream reported partial unmask")
		}
	}
	return sr.text, sr.args, nil
}

func trg01CheckDecision(c *trg01Case, d decisionView) error {
	if d.Privacy != c.v.mode {
		return fmt.Errorf("decision privacy %q, want %q", d.Privacy, c.v.mode)
	}
	for typ := range c.types {
		if d.Findings[typ] == 0 {
			return fmt.Errorf("decision findings %v miss %s", d.Findings, typ)
		}
	}
	for i, a := range d.Attempts {
		if c.v.local && a.Locality != "local" {
			return fmt.Errorf("attempt %d to %s (%s) under %s", i, a.Target, a.Locality, c.v.mode)
		}
		if a.Locality == "cloud" && !a.Masked {
			return fmt.Errorf("attempt %d to cloud target %s not masked", i, a.Target)
		}
	}
	if !c.v.cut {
		return nil
	}
	if len(d.Attempts) != 2 || !d.Attempts[1].Continuation {
		return fmt.Errorf("want a cut attempt and one continuation, got %+v", d.Attempts)
	}
	if wantCloud := !c.v.contLocal; wantCloud != (d.Attempts[1].Locality == "cloud") {
		return fmt.Errorf("continuation locality %s", d.Attempts[1].Locality)
	}
	return nil
}

// trg01LeakScan searches every body a cloud model received for every value
// of the run, raw and after JSON decoding (catches \u-escaped values).
type trg01LeakScan struct {
	leaked        map[string]string // value -> type
	cloudRequests int
	contBodies    int // cloud continuation bodies whose partial assistant text holds a placeholder
}

func trg01Scan(cases []*trg01Case, records []fakeRecord) trg01LeakScan {
	valueType := map[string]string{}
	for _, c := range cases {
		for i, v := range c.values {
			valueType[v] = c.valueTypes[i]
		}
	}
	s := trg01LeakScan{leaked: map[string]string{}}
	for _, rec := range records {
		if !trg01CloudModels[rec.Model] {
			continue
		}
		s.cloudRequests++
		decoded := trg01Strings(rec.Body)
		for v, typ := range valueType {
			if strings.Contains(rec.Body, v) || strings.Contains(decoded, v) {
				s.leaked[v] = typ
			}
		}
		if trg01IsMaskedContinuation(rec.Body) {
			s.contBodies++
		}
	}
	return s
}

// trg01Strings joins every string of a JSON document with NUL separators.
func trg01Strings(body string) string {
	var v any
	if json.Unmarshal([]byte(body), &v) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte(0)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for k, e := range t {
				b.WriteString(k)
				b.WriteByte(0)
				walk(e)
			}
		}
	}
	walk(v)
	return b.String()
}

func trg01IsMaskedContinuation(body string) bool {
	var b struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(body), &b) != nil || len(b.Messages) < 2 {
		return false
	}
	last, prev := b.Messages[len(b.Messages)-1], b.Messages[len(b.Messages)-2]
	return last.Content == dispatch.ContinuationInstruction && prev.Role == "assistant" &&
		strings.Contains(prev.Content, "[[")
}

// trg01Tally summarises the case plan.
type trg01Tally struct {
	types, formats, langs, byVariant                map[string]int
	maskCont, maskContNew, localCont, wantCloudReqs int
}

func trg01Count(cases []*trg01Case) trg01Tally {
	t := trg01Tally{
		types: map[string]int{}, formats: map[string]int{}, langs: map[string]int{}, byVariant: map[string]int{},
	}
	for _, c := range cases {
		for typ, n := range c.types {
			t.types[typ] += n
		}
		for _, fm := range c.formats {
			t.formats[fm]++
		}
		t.langs[c.lang]++
		t.byVariant[c.v.name]++
		switch {
		case c.v.cut && c.v.contLocal:
			t.localCont++
		case c.v.cut:
			t.maskCont++
			t.wantCloudReqs++
			if c.newValues > 0 {
				t.maskContNew++
			}
		case !c.v.local:
			t.wantCloudReqs++
		}
	}
	return t
}

func trg01Evaluate(tb testing.TB, th map[string]any, cases []*trg01Case, st *trg01Stats) (map[string]any, bool) {
	tb.Helper()
	scan := trg01Scan(cases, st.records)
	t := trg01Count(cases)
	leakedTypes := map[string]int{}
	for _, typ := range scan.leaked {
		leakedTypes[typ]++
	}
	contOK := t.maskCont - st.failedBy[trg01VariantMaskCut]
	measured := map[string]any{
		"cases": len(cases), "cases_by_variant": t.byVariant, "langs": t.langs,
		"pii_types": t.types, "pii_type_count": len(t.types),
		"pii_formats": t.formats, "pii_format_count": len(t.formats),
		"values": trg01ValueCount(cases), "leaked_values": len(scan.leaked), "leaked_by_type": leakedTypes,
		"dropped": st.dropped, "fakellm_records": len(st.records),
		"cloud_requests": scan.cloudRequests, "cloud_requests_expected": t.wantCloudReqs,
		"continuation_cases_mask_cloud": t.maskCont, "continuation_cases_local_only": t.localCont,
		"continuation_cases_pii_in_partial_ok":  contOK,
		"continuation_cases_new_pii_in_partial": t.maskContNew,
		"cloud_continuation_bodies_masked":      scan.contBodies,
		"failed_cases":                          st.nFailures,
	}
	pass := len(scan.leaked) <= int(num(tb, th, "leaked_values")) &&
		float64(len(cases)) >= num(tb, th, "cases_min") &&
		st.dropped <= int(num(tb, th, "dropped")) &&
		st.nFailures == 0 && contOK >= trg01MinCont && scan.contBodies == t.maskCont &&
		scan.cloudRequests == t.wantCloudReqs && len(t.types) == len(trg01Types) &&
		len(t.formats) >= trg01MinFormats && len(t.langs) == 3
	return measured, pass
}

func trg01ValueCount(cases []*trg01Case) int {
	seen := map[string]bool{}
	for _, c := range cases {
		for _, v := range c.values {
			seen[v] = true
		}
	}
	return len(seen)
}
