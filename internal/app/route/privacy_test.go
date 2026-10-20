// SPDX-License-Identifier: Apache-2.0

package route_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/testutil/fakestrategy"
)

func privacyRoute(mode, maxClient privacy.Mode) route.Route {
	r := baseRoute()
	r.Privacy = route.Privacy{Mode: mode, MaxClientMode: maxClient}
	return r
}

func TestPipeline_Chat_ClientPrivacyHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      privacy.Mode
		maxClient privacy.Mode
		header    string
		wantMode  privacy.Mode
		wantErr   bool
	}{
		{name: "stricter applied", mode: privacy.ModeOff, header: "local_only", wantMode: privacy.ModeLocalOnly},
		{name: "stricter mask applied", mode: privacy.ModeOff, header: " Mask ", wantMode: privacy.ModeMask},
		{name: "looser ignored", mode: privacy.ModeMask, header: "off", wantMode: privacy.ModeMask},
		{name: "equal ignored", mode: privacy.ModeMask, header: "mask", wantMode: privacy.ModeMask},
		{name: "empty ignored", mode: privacy.ModeMask, header: "", wantMode: privacy.ModeMask},
		{
			name: "stricter than max not allowed", mode: privacy.ModeOff, maxClient: privacy.ModeMask,
			header: "local_only", wantErr: true,
		},
		{
			name: "max equal to requested allowed", mode: privacy.ModeOff, maxClient: privacy.ModeMask,
			header: "mask", wantMode: privacy.ModeMask,
		},
		{
			name: "not stricter than route ignores max", mode: privacy.ModeLocalOnly, maxClient: privacy.ModeOff,
			header: "local_only", wantMode: privacy.ModeLocalOnly,
		},
		{name: "unknown value", mode: privacy.ModeOff, header: "paranoid", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, setup{routes: []route.Route{privacyRoute(tc.mode, tc.maxClient)}})
			out, err := e.p.Chat(context.Background(), withHeader(userReq("hi"), route.HeaderPrivacy, tc.header))
			d := e.only(t)
			if tc.wantErr {
				require.ErrorIs(t, err, privacy.ErrModeNotAllowed)
				code, status := route.Code(err)
				assert.Equal(t, "privacy_mode_not_allowed", code)
				assert.Equal(t, 400, status)
				assert.Equal(t, "privacy_mode_not_allowed", d.Error)
				assert.Equal(t, 0, e.disp.calls())
				assert.Equal(t, "d-1", out.DecisionID)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantMode, d.Privacy)
			assert.Empty(t, d.Error)
		})
	}
}

func TestPipeline_Chat_PrivacyLocalities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mode        privacy.Mode
		text        string
		wantChain   []string
		wantLocal   bool
		wantMapping bool
		wantDetect  bool
		wantReasons []string
		wantCounts  map[string]int
	}{
		{
			name: "local_only with finding", mode: privacy.ModeLocalOnly, text: "mail " + secret,
			wantChain: []string{"local-weak"}, wantLocal: true,
			wantReasons: []string{"privacy:local_only", "default:weak"}, wantCounts: map[string]int{"email": 1},
		},
		{
			name: "local_only without finding", mode: privacy.ModeLocalOnly, text: "hello",
			wantChain: []string{"local-weak", "cloud-weak"}, wantReasons: []string{"default:weak"},
			wantCounts: map[string]int{},
		},
		{
			name: "mask with finding", mode: privacy.ModeMask, text: "mail " + secret + " and " + secret,
			wantChain: []string{"local-weak", "cloud-weak"}, wantMapping: true, wantDetect: true,
			wantReasons: []string{"privacy:mask", "default:weak"}, wantCounts: map[string]int{"email": 2},
		},
		{
			name: "mask without finding", mode: privacy.ModeMask, text: "hello",
			wantChain: []string{"local-weak", "cloud-weak"}, wantDetect: true,
			wantReasons: []string{"default:weak"}, wantCounts: map[string]int{},
		},
		{
			name: "off does not scan", mode: privacy.ModeOff, text: "mail " + secret,
			wantChain: []string{"local-weak", "cloud-weak"}, wantReasons: []string{"default:weak"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			det := emailDetector()
			e := newEnv(t, setup{
				routes: []route.Route{privacyRoute(tc.mode, "")}, detectors: []privacy.Detector{det},
			})
			_, err := e.p.Chat(context.Background(), userReq(tc.text))
			require.NoError(t, err)
			plan := e.disp.lastPlan(t)
			assert.Equal(t, tc.wantChain, names(plan.Chain))
			assert.Equal(t, model.LocalitySet{Local: true, Cloud: !tc.wantLocal}, plan.Localities)
			assert.Equal(t, tc.wantMapping, plan.Mapping != nil)
			assert.Equal(t, tc.wantDetect, len(plan.Detectors) == 1)
			d := e.only(t)
			assert.Equal(t, tc.wantReasons, d.Reasons)
			assert.Equal(t, tc.wantCounts, d.Findings)
			assert.Equal(t, tc.mode, d.Privacy)
			if tc.mode == privacy.ModeOff {
				assert.Equal(t, 0, det.Calls())
			}
		})
	}
}

func TestPipeline_Chat_MaskMappingMasksFindings(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{
		routes: []route.Route{privacyRoute(privacy.ModeMask, "")}, detectors: []privacy.Detector{emailDetector()},
	})
	_, err := e.p.Chat(context.Background(), userReq("mail "+secret))
	require.NoError(t, err)
	plan := e.disp.lastPlan(t)
	require.NotNil(t, plan.Mapping)
	masked := plan.Mapping.MaskRequest(plan.Request)
	assert.Equal(t, "mail [[EMAIL_1]]", masked.LastUserText())
	assert.Equal(t, "mail "+secret, plan.Request.LastUserText(), "plan request stays original")
}

func TestPipeline_Chat_ScanSystem(t *testing.T) {
	t.Parallel()
	for _, scanSystem := range []bool{false, true} {
		e := newEnv(t, setup{
			routes:    []route.Route{privacyRoute(privacy.ModeLocalOnly, "")},
			detectors: []privacy.Detector{emailDetector()},
			opts:      route.Options{Privacy: route.PrivacyOptions{ScanSystem: scanSystem}},
		})
		req := userReq("hello")
		req.Messages[0] = chat.TextMessage(chat.RoleSystem, "admin is "+secret)
		_, err := e.p.Chat(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, !scanSystem, e.disp.lastPlan(t).Localities.Cloud, "scan_system=%v", scanSystem)
	}
}

func TestPipeline_Chat_Oversize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      privacy.Mode
		action    privacy.OversizeAction
		wantErr   error
		wantLocal bool
	}{
		{name: "mask local_only", mode: privacy.ModeMask, action: privacy.OversizeLocalOnly, wantLocal: true},
		{name: "local_only default action", mode: privacy.ModeLocalOnly, action: "", wantLocal: true},
		{name: "mask reject", mode: privacy.ModeMask, action: privacy.OversizeReject, wantErr: privacy.ErrScanLimit},
		{
			name: "local_only reject", mode: privacy.ModeLocalOnly, action: privacy.OversizeReject,
			wantErr: privacy.ErrScanLimit,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			det := emailDetector()
			e := newEnv(t, setup{
				routes:    []route.Route{privacyRoute(tc.mode, "")},
				detectors: []privacy.Detector{det},
				opts:      route.Options{Privacy: route.PrivacyOptions{MaxScanBytes: 8, OnOversize: tc.action}},
			})
			_, err := e.p.Chat(context.Background(), userReq("this text is longer than eight bytes"))
			d := e.only(t)
			assert.True(t, d.Oversize)
			assert.Len(t, e.obs.kinds(route.EventOversize), 1)
			assert.Equal(t, 0, det.Calls())
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				code, status := route.Code(err)
				assert.Equal(t, "privacy_scan_limit", code)
				assert.Equal(t, 413, status)
				assert.Equal(t, "privacy_scan_limit", d.Error)
				assert.Equal(t, 0, e.disp.calls())
				return
			}
			require.NoError(t, err)
			plan := e.disp.lastPlan(t)
			assert.Equal(t, model.LocalOnly(), plan.Localities)
			assert.Equal(t, []string{"local-weak"}, names(plan.Chain))
			assert.Nil(t, plan.Mapping)
			assert.Equal(t, []string{"privacy:local_only", "default:weak"}, d.Reasons)
		})
	}
}

func TestPipeline_Chat_DetectorErrorFailsClosed(t *testing.T) {
	t.Parallel()
	for _, mode := range []privacy.Mode{privacy.ModeMask, privacy.ModeLocalOnly} {
		det := emailDetector().WithError(errors.New("boom"))
		e := newEnv(t, setup{routes: []route.Route{privacyRoute(mode, "")}, detectors: []privacy.Detector{det}})
		_, err := e.p.Chat(context.Background(), userReq("hello"))
		require.NoError(t, err)
		plan := e.disp.lastPlan(t)
		assert.Equal(t, model.LocalOnly(), plan.Localities, "mode %s", mode)
		assert.Equal(t, []string{"local-weak"}, names(plan.Chain))
		assert.Nil(t, plan.Mapping)
		assert.Equal(t, []string{"privacy:scan_error", "privacy:local_only", "default:weak"}, e.only(t).Reasons)
	}
}

func mfSetup(det privacy.Detector, opts route.Options) (setup, *fakestrategy.Strategy, *fakestrategy.Strategy) {
	mf := fakestrategy.New("mf", func(context.Context, chat.Request) (difficulty.Score, error) {
		return difficulty.Score{P: 0.9, EmbeddingInputTokens: 500_000}, nil
	})
	bert := fakestrategy.Fixed("bert", 0.1)
	r := baseRoute()
	r.Strategy = route.StrategyMF
	r.Threshold = 0.5
	opts.Difficulty.MFFallback = "bert"
	opts.Difficulty.MFCostTarget = "cloud-embed"
	targets := allTargets()
	targets[4].Pricing.InputPerMTok = 0.02
	return setup{
		targets: targets, routes: []route.Route{r}, detectors: []privacy.Detector{det}, opts: opts,
		strategies: map[string]difficulty.Strategy{"mf": mf, "bert": bert},
	}, mf, bert
}

func TestPipeline_Chat_MFUnderOff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		text        string
		det         *fakedetectorOpt
		maxBytes    int
		wantMF      bool
		wantReasons []string
		wantTier    model.Tier
		wantCost    float64
	}{
		{
			name: "no finding uses mf", text: "hello", wantMF: true, wantTier: model.TierStrong,
			wantReasons: []string{"strategy:mf p=0.90>=0.50"}, wantCost: 0.01,
		},
		{
			name: "finding falls back", text: "mail " + secret, wantTier: model.TierWeak,
			wantReasons: []string{"strategy:mf->bert(privacy)", "strategy:bert p=0.10<0.50"},
		},
		{
			name: "oversize falls back and never rejects", text: "long enough text", maxBytes: 4,
			wantTier: model.TierWeak, wantReasons: []string{"strategy:mf->bert(privacy)", "strategy:bert p=0.10<0.50"},
		},
		{
			name: "scan error falls back", text: "hello", det: &fakedetectorOpt{err: errors.New("boom")},
			wantTier: model.TierWeak,
			wantReasons: []string{
				"privacy:scan_error", "strategy:mf->bert(privacy)", "strategy:bert p=0.10<0.50",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			det := emailDetector()
			if tc.det != nil {
				det.WithError(tc.det.err)
			}
			s, mf, bert := mfSetup(det, route.Options{Privacy: route.PrivacyOptions{
				MaxScanBytes: tc.maxBytes, OnOversize: privacy.OversizeReject,
			}})
			e := newEnv(t, s)
			_, err := e.p.Chat(context.Background(), userReq(tc.text))
			require.NoError(t, err)
			plan := e.disp.lastPlan(t)
			assert.Equal(t, model.AllLocalities(), plan.Localities, "off never constrains routing")
			assert.Nil(t, plan.Mapping)
			assert.Nil(t, plan.Detectors)
			d := e.only(t)
			assert.Equal(t, tc.wantReasons, d.Reasons)
			assert.Equal(t, tc.wantTier, d.Tier)
			assert.Equal(t, privacy.ModeOff, d.Privacy)
			assert.InDelta(t, tc.wantCost, d.ScoringCostUSD, 1e-12)
			assert.InDelta(t, 0.5+tc.wantCost, d.CostUSD, 1e-12)
			if tc.wantMF {
				assert.Equal(t, 1, mf.Calls())
				assert.Equal(t, 0, bert.Calls())
				assert.Equal(t, "mf", d.Strategy)
				assert.Len(t, e.obs.kinds(route.EventMFExternalEmbedding), 1)
				return
			}
			assert.Equal(t, 0, mf.Calls(), "scored text must not leave the machine")
			assert.Equal(t, 1, bert.Calls())
			assert.Equal(t, "bert", d.Strategy)
			assert.Empty(t, e.obs.kinds(route.EventMFExternalEmbedding))
		})
	}
}

type fakedetectorOpt struct{ err error }

func TestPipeline_Chat_MFUnderMaskConstrainsAndFallsBack(t *testing.T) {
	t.Parallel()
	s, mf, _ := mfSetup(emailDetector(), route.Options{})
	s.routes[0].Privacy.Mode = privacy.ModeMask
	e := newEnv(t, s)
	_, err := e.p.Chat(context.Background(), userReq("mail "+secret))
	require.NoError(t, err)
	plan := e.disp.lastPlan(t)
	assert.NotNil(t, plan.Mapping)
	assert.Equal(t, 0, mf.Calls())
	assert.Equal(t,
		[]string{"privacy:mask", "strategy:mf->bert(privacy)", "strategy:bert p=0.10<0.50"}, e.only(t).Reasons)
}

func TestPipeline_Chat_AdoptsContextDecisionID(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{})
	out, err := e.p.Chat(route.WithDecisionID(context.Background(), "announced-1"), userReq("hello"))
	require.NoError(t, err)
	assert.Equal(t, "announced-1", out.DecisionID)
}
