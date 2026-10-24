// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/testutil/fakedetector"
	"github.com/thnhdatle/triage/internal/testutil/fakerecorder"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var epoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

const (
	secret    = "bob@example.com"
	routeName = "auto"
	waitLimit = 5 * time.Second
)

func target(name string, loc model.Locality, tier model.Tier, window int, caps ...model.Capability) model.Target {
	return model.Target{
		Name: name, Provider: name, Model: name + "-model", Locality: loc, Tier: tier,
		ContextWindow: window, Capabilities: caps,
	}
}

var (
	localWeak   = target("local-weak", model.LocalityLocal, model.TierWeak, 8192, model.CapTools)
	localStrong = target("local-strong", model.LocalityLocal, model.TierStrong, 32768)
	cloudWeak   = target("cloud-weak", model.LocalityCloud, model.TierWeak, 16384)
	cloudStrong = target("cloud-strong", model.LocalityCloud, model.TierStrong, 200000,
		model.CapTools, model.CapVision, model.CapJSONMode)
	cloudEmbed = target("cloud-embed", model.LocalityCloud, model.TierNone, 8192, model.CapEmbeddings)
	localEmbed = target("local-embed", model.LocalityLocal, model.TierNone, 8192, model.CapEmbeddings)
)

func allTargets() model.Pool {
	return model.Pool{localWeak, localStrong, cloudWeak, cloudStrong, cloudEmbed, localEmbed}
}

func baseRoute() route.Route {
	return route.Route{Name: routeName, Privacy: route.Privacy{Mode: privacy.ModeOff}, Strategy: route.StrategyNone}
}

func userReq(text string) chat.Request {
	return chat.Request{
		ID: "req-1", Model: routeName,
		Messages: []chat.Message{
			chat.TextMessage(chat.RoleSystem, "be brief"),
			chat.TextMessage(chat.RoleUser, text),
		},
	}
}

func withHeader(req chat.Request, k, v string) chat.Request {
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	req.Headers[k] = v
	return req
}

func emailDetector() *fakedetector.Detector {
	return fakedetector.NewLiteral("fake", "email", secret)
}

type fakeDispatcher struct {
	mu    sync.Mutex
	plans []dispatch.Plan
	fn    func(plan dispatch.Plan) (dispatch.Result, error)
}

func okResult(plan dispatch.Plan) dispatch.Result {
	t := plan.Chain[0]
	u := chat.Usage{PromptTokens: 100, CompletionTokens: 10}
	return dispatch.Result{
		Response: chat.Response{Model: t.Model, Usage: u},
		Target:   t, Model: t.Model,
		Attempts: []decision.Attempt{{Target: t.Name, Model: t.Model, Locality: t.Locality, Status: "ok", Usage: u}},
		Usage:    u,
		CostUSD:  0.5,
	}
}

func (f *fakeDispatcher) do(plan dispatch.Plan) (dispatch.Result, error) {
	f.mu.Lock()
	f.plans = append(f.plans, plan)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(plan)
	}
	return okResult(plan), nil
}

func (f *fakeDispatcher) Complete(_ context.Context, plan dispatch.Plan) (dispatch.Result, error) {
	return f.do(plan)
}

func (f *fakeDispatcher) Stream(_ context.Context, plan dispatch.Plan, sink dispatch.Sink) (dispatch.Result, error) {
	if err := sink.Start(plan.Chain[0]); err != nil {
		return dispatch.Result{}, err
	}
	return f.do(plan)
}

func (f *fakeDispatcher) lastPlan(t testingT) dispatch.Plan {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.plans, "dispatcher was not called")
	return f.plans[len(f.plans)-1]
}

func (f *fakeDispatcher) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.plans)
}

type healthSet map[string]bool

func (h healthSet) IsHealthy(name string) bool { return !h[name] }

type observer struct {
	mu     sync.Mutex
	events []route.Event
}

func (o *observer) Observe(e route.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func (o *observer) kinds(k route.EventKind) []route.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []route.Event
	for _, e := range o.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

type counter struct{ n int }

func (c counter) CountTokens(string) int { return c.n }

type setup struct {
	targets    model.Pool
	routes     []route.Route
	strategies map[string]difficulty.Strategy
	detectors  []privacy.Detector
	opts       route.Options
	health     route.Health
	embedders  map[string]route.Embedder
	dispatcher route.Dispatcher
}

type env struct {
	p    *route.Pipeline
	disp *fakeDispatcher
	rec  *fakerecorder.Recorder
	clk  *clock.Fake
	obs  *observer
}

type testingT interface {
	require.TestingT
	Helper()
}

func newEnv(t testingT, s setup) env {
	t.Helper()
	if s.targets == nil {
		s.targets = allTargets()
	}
	if s.routes == nil {
		s.routes = []route.Route{baseRoute()}
	}
	e := env{disp: &fakeDispatcher{}, rec: fakerecorder.New(), clk: clock.NewFake(epoch), obs: &observer{}}
	var disp route.Dispatcher = e.disp
	if s.dispatcher != nil {
		disp = s.dispatcher
	}
	var ids atomic.Int64
	p, err := route.New(route.Deps{
		Targets: s.targets, Routes: s.routes, Strategies: s.strategies, Detectors: s.detectors,
		Dispatcher: disp, Embedders: s.embedders, Recorder: e.rec, Health: s.health, Clock: e.clk,
		NewID:    func() string { return "d-" + strconv.FormatInt(ids.Add(1), 10) },
		Observer: e.obs,
	}, s.opts)
	require.NoError(t, err)
	e.p = p
	return e
}

func (e env) only(t testingT) decision.Decision {
	t.Helper()
	ds := e.rec.Decisions()
	require.Len(t, ds, 1)
	return ds[0]
}

func names(ts []model.Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

type sink struct {
	mu      sync.Mutex
	started []string
	chunks  []chat.Chunk
}

func (s *sink) Start(t model.Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, t.Name)
	return nil
}

func (s *sink) Chunk(c chat.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = append(s.chunks, c)
	return nil
}

func (s *sink) Comment(string) error { return nil }
func (s *sink) Error(error) error    { return nil }

func provErr(kind error, status int) error {
	return &provider.Error{Kind: kind, StatusCode: status, Message: "scripted"}
}
