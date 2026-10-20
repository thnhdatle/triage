// SPDX-License-Identifier: Apache-2.0

package dispatch_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil/fakedetector"
	"github.com/thnhdatle/triage/internal/testutil/fakeprovider"
)

// discard is a Sink that accepts everything.
type discard struct{}

func (discard) Start(model.Target) error { return nil }
func (discard) Chunk(chat.Chunk) error   { return nil }
func (discard) Comment(string) error     { return nil }
func (discard) Error(error) error        { return nil }

// propOutcome draws one scripted upstream outcome. Stream outputs repeat the
// finding values, as a model echoing its prompt would, so continuation
// bodies carry them.
func propOutcome(t *rapid.T, values []string, label string) fakeprovider.Outcome {
	v := rapid.SampledFrom(values).Draw(t, label+"-value")
	switch rapid.IntRange(0, 5).Draw(t, label+"-kind") {
	case 0:
		return fakeprovider.Fail(errRate)
	case 1:
		return fakeprovider.Fail(errServer)
	case 2:
		return fakeprovider.Fail(errBad)
	case 3:
		k := rapid.IntRange(0, 3).Draw(t, label+"-k")
		parts := []string{"x " + v, " y", " " + v, " z"}[:k]
		return stream(cutChunks("up", parts...), errUnavailable)
	case 4:
		return stream(cutChunks("up", "note "+v+" end"), errUnavailable)
	}
	return stream(textChunks("up", "ok "+v), nil)
}

// propChain draws a chain of 1-5 local or cloud targets, each with its own
// scripted fake provider.
func propChain(t *rapid.T, values []string) ([]model.Target, map[string]*fakeprovider.Provider) {
	n := rapid.IntRange(1, 5).Draw(t, "targets")
	chain := make([]model.Target, 0, n)
	fakes := map[string]*fakeprovider.Provider{}
	for i := range n {
		name := fmt.Sprintf("t%d", i)
		tg := local(name)
		if rapid.Bool().Draw(t, name+"-cloud") {
			tg = cloud(name)
		}
		chain = append(chain, tg)
		k := rapid.IntRange(0, 3).Draw(t, name+"-outcomes")
		outs := make([]fakeprovider.Outcome, 0, k)
		for j := range k {
			outs = append(outs, propOutcome(t, values, fmt.Sprintf("%s-%d", name, j)))
		}
		fakes[name] = scripted(outs...)
	}
	return chain, fakes
}

// propPlan draws the request, localities and mask mapping of a plan.
func propPlan(t *rapid.T, values []string, chain []model.Target) dispatch.Plan {
	req := userRequest("contact " + strings.Join(values, " and ") + " today")
	if rapid.Bool().Draw(t, "tool-history") {
		req.Messages = append(req.Messages, chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{
			ID: "c1", Type: "function",
			Function: chat.FunctionCall{Name: "send", Arguments: `{"to":"` + values[0] + `"}`},
		}}})
	}
	det := fakedetector.NewLiteral("fake", "email", values...)
	scan, err := privacy.Scan(context.Background(), []privacy.Detector{det}, req, privacy.ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := dispatch.Plan{
		Request: req, Chain: chain,
		Localities: model.LocalitySet{
			Local: rapid.Bool().Draw(t, "allow-local"), Cloud: rapid.Bool().Draw(t, "allow-cloud"),
		},
	}
	if rapid.Bool().Draw(t, "mask") {
		p.Mapping = privacy.NewMapping(req, scan.Findings)
		p.Detectors = []privacy.Detector{det}
	} else {
		// Without a mask mapping (local_only mode) the request may only go local.
		p.Localities.Cloud = false
	}
	return p
}

// checkPrivacy fails when a target outside the localities got a request or a
// cloud target got a finding value.
func checkPrivacy(t *rapid.T, p dispatch.Plan, fakes map[string]*fakeprovider.Provider, values []string) {
	for _, tg := range p.Chain {
		reqs := fakes[tg.Name].Requests()
		if len(reqs) > 0 && !p.Localities.Allows(tg.Locality) {
			t.Fatalf("target %s (%s) got a request outside the allowed localities", tg.Name, tg.Locality)
		}
		if tg.Locality != model.LocalityCloud {
			continue
		}
		for _, r := range reqs {
			body := requestText(r)
			for _, v := range values {
				if strings.Contains(body, v) {
					t.Fatalf("cloud target %s received finding value %q", tg.Name, v)
				}
			}
		}
	}
}

// TestDispatcher_Property_PrivacyHolds checks in any fallback
// path, no request reaches a target outside the allowed localities and no
// request to a cloud target (continuation bodies included) contains a
// finding value.
func TestDispatcher_Property_PrivacyHolds(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		values := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{3,6}@[a-z]{2,4}\.com`), 1, 3,
			rapid.ID[string]).Draw(t, "values")
		chain, fakes := propChain(t, values)
		providers := make(map[string]provider.Provider, len(fakes))
		for name, f := range fakes {
			providers[name] = f
		}
		p := propPlan(t, values, chain)
		d, err := dispatch.New(providers, clock.Real(), dispatch.Options{
			MaxAttempts:   rapid.IntRange(1, 5).Draw(t, "max-attempts"),
			Backoff:       dispatch.Backoff{Base: time.Nanosecond, Max: time.Nanosecond},
			StreamFailure: rapid.SampledFrom([]dispatch.StreamFailure{"continue", "error"}).Draw(t, "failure"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if rapid.Bool().Draw(t, "stream") {
			_, _ = d.Stream(context.Background(), p, discard{})
		} else {
			_, _ = d.Complete(context.Background(), p)
		}
		checkPrivacy(t, p, fakes, values)
	})
}
