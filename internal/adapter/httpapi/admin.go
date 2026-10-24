// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/platform/buildinfo"
)

const (
	defaultLimit = 50
	maxLimit     = 500
)

type decisionJSON struct {
	ID               string         `json:"id"`
	Route            string         `json:"route"`
	Time             time.Time      `json:"time"`
	Operation        string         `json:"operation"`
	Privacy          string         `json:"privacy"`
	Findings         map[string]int `json:"findings,omitempty"`
	Oversize         bool           `json:"oversize"`
	Strategy         string         `json:"strategy,omitempty"`
	Variant          string         `json:"variant,omitempty"`
	Score            *float64       `json:"score,omitempty"`
	Threshold        *float64       `json:"threshold,omitempty"`
	ScoringLatencyMS float64        `json:"scoring_latency_ms"`
	ScoringCostUSD   float64        `json:"scoring_cost_usd"`
	Tier             string         `json:"tier,omitempty"`
	Reasons          []string       `json:"reasons"`
	Attempts         []attemptJSON  `json:"attempts"`
	Unmask           string         `json:"unmask,omitempty"`
	Usage            usageJSON      `json:"usage"`
	CostUSD          float64        `json:"cost_usd"`
	Error            string         `json:"error,omitempty"`
}

type attemptJSON struct {
	Target       string    `json:"target"`
	Model        string    `json:"model"`
	Locality     string    `json:"locality"`
	Masked       bool      `json:"masked"`
	Continuation bool      `json:"continuation"`
	Status       string    `json:"status"`
	HTTPStatus   int       `json:"http_status,omitempty"`
	LatencyMS    float64   `json:"latency_ms"`
	Usage        usageJSON `json:"usage"`
	CostUSD      float64   `json:"cost_usd"`
}

type usageJSON struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	CachedPromptTokens int `json:"cached_prompt_tokens"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func usageView(u chat.Usage) usageJSON {
	return usageJSON{u.PromptTokens, u.CompletionTokens, u.CachedPromptTokens}
}

func decisionView(d decision.Decision) decisionJSON {
	out := decisionJSON{
		ID: d.ID, Route: d.Route, Time: d.Time, Operation: d.Operation, Privacy: string(d.Privacy),
		Findings: d.Findings, Oversize: d.Oversize, Strategy: d.Strategy, Variant: d.Variant, Score: d.Score,
		Threshold: d.Threshold, ScoringLatencyMS: ms(d.ScoringLatency), ScoringCostUSD: d.ScoringCostUSD,
		Tier: string(d.Tier), Reasons: append([]string{}, d.Reasons...), Attempts: make([]attemptJSON, 0, len(d.Attempts)),
		Unmask: string(d.Unmask), Usage: usageView(d.Usage), CostUSD: d.CostUSD, Error: d.Error,
	}
	for _, a := range d.Attempts {
		out.Attempts = append(out.Attempts, attemptJSON{
			Target: a.Target, Model: a.Model, Locality: string(a.Locality), Masked: a.Masked,
			Continuation: a.Continuation, Status: a.Status, HTTPStatus: a.HTTPStatus, LatencyMS: ms(a.Latency),
			Usage: usageView(a.Usage), CostUSD: a.CostUSD,
		})
	}
	return out
}

func (s *Server) handleDecisionList(w http.ResponseWriter, r *http.Request) {
	limit := defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			writeAPIError(w, invalid("limit", "limit must be an integer between 1 and "+strconv.Itoa(maxLimit)))
			return
		}
		limit = n
	}
	page, next, err := s.decisions.List(limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeAPIError(w, &apiError{
			status: http.StatusBadRequest, code: CodeInvalidCursor, param: "cursor",
			message: "the cursor is not valid",
		})
		return
	}
	data := make([]decisionJSON, 0, len(page))
	for _, d := range page {
		data = append(data, decisionView(d))
	}
	writeJSON(w, http.StatusOK, struct {
		Data       []decisionJSON `json:"data"`
		NextCursor string         `json:"next_cursor,omitempty"`
	}{data, next})
}

func (s *Server) handleDecisionGet(w http.ResponseWriter, r *http.Request) {
	d, ok := s.decisions.Get(r.PathValue("id"))
	if !ok {
		writeAPIError(w, &apiError{
			status: http.StatusNotFound, code: CodeDecisionNotFound,
			message: "no decision with that id (it may have left the ring buffer)",
		})
		return
	}
	writeJSON(w, http.StatusOK, decisionView(d))
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Redacted())
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := s.reloader.Reload(r.Context()); err != nil {
		writeAPIError(w, &apiError{
			status: http.StatusUnprocessableEntity, code: CodeConfigInvalid,
			message: truncate("reload rejected, the previous configuration stays active: " + err.Error()),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

type adminModelJSON struct {
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Locality      string   `json:"locality"`
	Tier          string   `json:"tier"`
	ContextWindow int      `json:"context_window"`
	Capabilities  []string `json:"capabilities"`
	Healthy       *bool    `json:"healthy,omitempty"`
}

func (s *Server) handleAdminModels(w http.ResponseWriter, _ *http.Request) {
	targets := s.catalog.Targets()
	out := make([]adminModelJSON, 0, len(targets))
	for _, t := range targets {
		m := adminModelJSON{
			Name: t.Name, Provider: t.Provider, Model: t.Model, Locality: string(t.Locality), Tier: string(t.Tier),
			ContextWindow: t.ContextWindow, Capabilities: []string{},
		}
		for _, c := range t.Capabilities {
			m.Capabilities = append(m.Capabilities, string(c))
		}
		if s.health != nil {
			h := s.health.IsHealthy(t.Name)
			m.Healthy = &h
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, struct {
		Routes  []string         `json:"routes"`
		Targets []adminModelJSON `json:"targets"`
	}{append([]string{}, s.catalog.Routes()...), out})
}

type statusJSON struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if s.shuttingDown.Load() {
		writeJSON(w, http.StatusServiceUnavailable, statusJSON{"shutting_down", ""})
		return
	}
	writeJSON(w, http.StatusOK, statusJSON{"ok", ""})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.shuttingDown.Load() {
		writeJSON(w, http.StatusServiceUnavailable, statusJSON{"unavailable", "shutting down"})
		return
	}
	if s.ready != nil {
		if err := s.ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, statusJSON{"unavailable", truncate(err.Error())})
			return
		}
	}
	writeJSON(w, http.StatusOK, statusJSON{"ok", ""})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildinfo.Get())
}
