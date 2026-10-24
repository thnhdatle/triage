// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

const (
	ScenarioOK          = "ok"
	ScenarioEcho        = "echo"
	ScenarioToolCall    = "tool-call"
	ScenarioSlow        = "slow"
	ScenarioTimeout     = "timeout"
	Scenario429         = "429"
	Scenario500         = "500"
	ScenarioCutStream   = "cut-stream"
	ScenarioInvalidJSON = "invalid-json"
)

const maxScriptRules = 1000

var knownScenarios = []string{
	ScenarioOK, ScenarioEcho, ScenarioToolCall, ScenarioSlow, ScenarioTimeout,
	Scenario429, Scenario500, ScenarioCutStream, ScenarioInvalidJSON,
}

type scriptParams struct {
	CutAfter   *int    `json:"cut_after"`
	LatencyMs  *int    `json:"latency_ms"`
	Answer     *string `json:"answer"`
	ChunkBytes *int    `json:"chunk_bytes"`
}

type scriptEntry struct {
	Model    string       `json:"model"`
	Scenario string       `json:"scenario"`
	Count    int          `json:"count"`
	Params   scriptParams `json:"params"`
}

type scriptRule struct {
	scriptEntry
	remaining int
}

func (e scriptEntry) validate() error {
	switch {
	case e.Model == "":
		return errors.New("model is required")
	case !slices.Contains(knownScenarios, e.Scenario):
		return fmt.Errorf("unknown scenario %q", e.Scenario)
	case e.Count < 1:
		return errors.New("count must be at least 1")
	}
	return nil
}

func (s *Server) handlePutScript(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxScriptBytes)
	if !ok {
		return
	}
	var entries []scriptEntry
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		badRequest(w, "invalid script: "+err.Error())
		return
	}
	if len(entries) > maxScriptRules {
		badRequest(w, "too many script rules")
		return
	}
	for i, e := range entries {
		if err := e.validate(); err != nil {
			badRequest(w, fmt.Sprintf("script rule %d: %v", i, err))
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.models {
		st.rules = nil
	}
	for _, e := range entries {
		st, ok := s.state(e.Model)
		if !ok {
			badRequest(w, "too many distinct models")
			return
		}
		st.rules = append(st.rules, &scriptRule{scriptEntry: e, remaining: e.Count})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteScript(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.models {
		st.rules = nil
		st.count = 0
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) begin(model string, headerScenario bool) (n int, rule *scriptRule, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state(model)
	if !ok {
		return 0, nil, false
	}
	st.count++
	if headerScenario {
		return st.count, nil, true
	}
	for len(st.rules) > 0 && st.rules[0].remaining <= 0 {
		st.rules = st.rules[1:]
	}
	if len(st.rules) > 0 {
		rule = st.rules[0]
		rule.remaining--
		cp := *rule
		return st.count, &cp, true
	}
	return st.count, nil, true
}
