// SPDX-License-Identifier: Apache-2.0

//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	trg04Requests    = 1000
	trg04CutMin      = 1
	trg04CutMax      = 20
	trg04AnswerBytes = 512
	trg04StrongModel = "trg04-strong"
	trg04EmailDomain = "@pii-trg04.example.com"
)

// trg04Req is one scripted request: its route, and so the upstream model of
// the weak target whose scripted failure is its first attempt.
type trg04Req struct {
	idx    int
	stream bool
	kind   string // 429, 500, timeout, cut-<n>
	email  string
}

func (r trg04Req) route() string  { return "trg04-" + r.kind }
func (r trg04Req) target() string { return "weak-" + r.kind }
func (r trg04Req) model() string  { return "trg04-w-" + r.kind }

// label names the failure kind with the cut_after value folded away.
func (r trg04Req) label() string {
	k := r.kind
	if strings.HasPrefix(k, "cut-") {
		k = "cut-stream"
	}
	if r.stream {
		return "stream-" + k
	}
	return "json-" + k
}

// trg04Plan assigns the failure kinds: equally frequent within each half
// (non-streaming 429/500/timeout, streaming 429/500/timeout/cut-stream with a
// seeded cut_after uniform in 1-20), then shuffles the request order.
func trg04Plan() []trg04Req {
	rng := rand.New(rand.NewPCG(4, 4)) //nolint:gosec // seeded workload generator, not security
	half := trg04Requests / 2
	reqs := make([]trg04Req, 0, trg04Requests)
	for i := range half {
		reqs = append(reqs, trg04Req{stream: false, kind: []string{"429", "500", "timeout"}[i%3]})
	}
	for i := range half {
		kind := []string{"429", "500", "timeout", "cut"}[i%4]
		if kind == "cut" {
			kind = fmt.Sprintf("cut-%d", trg04CutMin+rng.IntN(trg04CutMax-trg04CutMin+1))
		}
		reqs = append(reqs, trg04Req{stream: true, kind: kind})
	}
	rng.Shuffle(len(reqs), func(i, j int) { reqs[i], reqs[j] = reqs[j], reqs[i] })
	for i := range reqs {
		reqs[i].idx = i
		reqs[i].email = fmt.Sprintf("user%04d%s", i, trg04EmailDomain)
	}
	return reqs
}

// trg04Script is one PUT /_fake/script rule per weak model, counting the
// requests of that kind so each request's first attempt consumes one failure.
func trg04Script(reqs []trg04Req) ([]map[string]any, map[string]int) {
	counts := map[string]int{}
	var order []string
	for _, r := range reqs {
		if counts[r.kind] == 0 {
			order = append(order, r.kind)
		}
		counts[r.kind]++
	}
	answer := strings.Repeat("0123456789abcdef", trg04AnswerBytes/16)
	rules := make([]map[string]any, 0, len(order))
	for _, kind := range order {
		rule := map[string]any{"model": "trg04-w-" + kind, "count": counts[kind]}
		var cut int
		if _, err := fmt.Sscanf(kind, "cut-%d", &cut); err == nil {
			rule["scenario"] = "cut-stream"
			rule["params"] = map[string]any{"cut_after": cut, "answer": answer}
		} else {
			rule["scenario"] = kind
		}
		rules = append(rules, rule)
	}
	return rules, counts
}

func trg04Config(f *fake, reqs []trg04Req) string {
	var targets, routes strings.Builder
	seen := map[string]bool{}
	for _, r := range reqs {
		if seen[r.kind] {
			continue
		}
		seen[r.kind] = true
		fmt.Fprintf(&targets, `  - { name: %s, provider: fake, model: %s, locality: local, tier: weak,
      context_window: 32768, capabilities: [tools, json_mode], extra_headers: {},
      pricing: { input_per_mtok: 0, output_per_mtok: 0 } }
`, r.target(), r.model())
		fmt.Fprintf(&routes, `  - name: %s
    pool: [%s, cloud-strong]
    strategy: none
    privacy: { mode: mask }
    default_tier: weak
    fallback: ["tier:strong"]
`, r.route(), r.target())
	}
	// Passive health would eject a weak target after a few consecutive
	// scripted failures and skip the failing first attempt; the threshold is
	// raised so every request's first attempt reaches its scripted failure.
	base := strings.Replace(targetsYAML(f, "trg04", "{}", "{}"),
		"health: { interval: 1h }", "health: { interval: 1h, failure_threshold: 1000000 }", 1)
	return base + targets.String() + `
dispatch: { attempt_timeout: 500ms, total_timeout: 20s, backoff: { base: 1ms, max: 5ms } }
routes:
` + routes.String()
}

type trg04Outcome struct {
	req        trg04Req
	complete   bool
	why        string
	decisionID string
}

func trg04Run(ctx context.Context, tr *triage, r trg04Req) trg04Outcome {
	out := trg04Outcome{req: r}
	text := fmt.Sprintf("request %d: please reply, my contact is %s", r.idx, r.email)
	resp, err := tr.chat(ctx, chatBody(r.route(), text, r.stream))
	if err != nil {
		out.why = "transport: " + err.Error()
		return out
	}
	out.decisionID = resp.header.Get("X-Triage-Decision")
	if resp.status != http.StatusOK {
		out.why = fmt.Sprintf("HTTP %d: %.200s", resp.status, resp.body)
		return out
	}
	finish := ""
	if r.stream {
		s, err := parseStream(resp.body)
		switch {
		case err != nil:
			out.why = err.Error()
			return out
		case !s.done:
			out.why = "no [DONE]"
			return out
		case s.errorEvent:
			out.why = "SSE error event"
			return out
		}
		finish = s.finishReason
	} else {
		var cr chatResponse
		if err := json.Unmarshal(resp.body, &cr); err != nil || len(cr.Choices) == 0 {
			out.why = fmt.Sprintf("bad body: %v %.200s", err, resp.body)
			return out
		}
		finish = cr.Choices[0].FinishReason
	}
	switch finish {
	case "stop", "length", "tool_calls":
		out.complete = true
	default:
		out.why = fmt.Sprintf("finish_reason %q", finish)
	}
	return out
}

// trg04Drive sends every request through a pool of concurrent clients.
func trg04Drive(tr *triage, reqs []trg04Req, concurrency int) []trg04Outcome {
	outs := make([]trg04Outcome, len(reqs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				outs[i] = trg04Run(context.Background(), tr, reqs[i])
			}
		}()
	}
	for i := range reqs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return outs
}

// trg04Verify checks from the decisions that every request's first attempt
// was its scripted failure, and counts completes and failures by kind.
func trg04Verify(tr *triage, outs []trg04Outcome) (byKind map[string]int, failures, completes int, problems []string) {
	byKind = map[string]int{}
	note := func(format string, a ...any) {
		if len(problems) < 20 {
			problems = append(problems, fmt.Sprintf(format, a...))
		}
	}
	for i := range outs {
		o := &outs[i]
		if o.complete {
			completes++
		} else {
			note("request %d (%s): %s", o.req.idx, o.req.kind, o.why)
		}
		if o.decisionID == "" {
			continue
		}
		d, _, err := tr.decision(context.Background(), o.decisionID)
		if err != nil {
			note("request %d: %v", o.req.idx, err)
			continue
		}
		if len(d.Attempts) < 2 || d.Attempts[0].Target != o.req.target() || d.Attempts[0].Status == "ok" {
			note("request %d (%s): first attempt not a scripted failure: %+v", o.req.idx, o.req.kind, d.Attempts)
			continue
		}
		failures++
		byKind[o.req.label()]++
	}
	return byKind, failures, completes, problems
}

// TestAcceptance_TRG04_Fallback implements TRG-04.
//
//nolint:paralleltest // acceptance tests share the compose stack and run one at a time
func TestAcceptance_TRG04_Fallback(t *testing.T) {
	th := thresholds(t, "TRG-04")
	minRatio := num(t, th, "complete_ratio_min")
	maxViolations := int(num(t, th, "privacy_violations"))
	wantFailures := int(num(t, th, "scripted_failures"))
	concurrency := int(num(t, th, "concurrency"))

	f := composeFake(t)
	reqs := trg04Plan()
	require.Len(t, reqs, wantFailures)
	rules, counts := trg04Script(reqs)
	f.script(t, rules)
	tr := startTriage(t, trg04Config(f, reqs))

	outs := trg04Drive(tr, reqs, concurrency)
	byKind, failures, completes, problems := trg04Verify(tr, outs)

	// Privacy: no email of any request may appear in a body sent to the cloud.
	recs, dropped := f.requests(t)
	violations, cloudBodies := 0, 0
	for _, rec := range recs {
		if rec.Model != trg04StrongModel {
			continue
		}
		cloudBodies++
		if strings.Contains(rec.Body, trg04EmailDomain) {
			violations++
		}
	}

	ratio := float64(completes) / float64(len(reqs))
	measured := map[string]any{
		"requests": len(reqs), "complete_ratio": ratio, "completed": completes,
		"privacy_violations": violations, "scripted_failures": failures,
		"concurrency": concurrency, "cloud_requests": cloudBodies, "fake_dropped": dropped,
		"by_kind": byKind, "scripted_per_model": counts,
	}
	pass := ratio >= minRatio && violations <= maxViolations && failures == wantFailures &&
		dropped == 0 && cloudBodies > 0
	writeResult(t, Result{ID: "TRG-04", Measured: measured, Pass: pass, Details: strings.Join(problems, "; ")})
	require.True(t, pass, "measured %v; problems: %v", measured, problems)
}
