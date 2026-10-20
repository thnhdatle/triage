// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/thnhdatle/triage/internal/platform/clock"
)

const (
	// DefaultModel is always listed by GET /v1/models.
	DefaultModel = "fake-default"
	// DefaultRecordLimit is the default size of the request log ring buffer.
	DefaultRecordLimit = 10000

	keyObject  = "object"
	keyModel   = "model"
	objectList = "list"

	maxModels      = 10000
	maxBodyBytes   = 32 << 20
	maxScriptBytes = 1 << 20
)

// Option configures a Server.
type Option func(*Server)

// WithRecord enables or disables the request log and sets its ring buffer
// size. The limit is only validated when recording is enabled.
func WithRecord(enabled bool, limit int) Option {
	return func(s *Server) {
		s.recordEnabled = enabled
		s.recordLimit = limit
	}
}

// WithClock injects the clock used for delays and timestamps.
func WithClock(c clock.Clock) Option {
	return func(s *Server) { s.clock = c }
}

// Server is the fake provider. All exported methods are safe for concurrent
// use.
type Server struct {
	clock         clock.Clock
	recordEnabled bool
	recordLimit   int
	log           *recorder

	// mu protects models.
	mu     sync.Mutex
	models map[string]*modelState
}

// modelState is the per-model counter and script queue; guarded by Server.mu.
type modelState struct {
	count int
	rules []*scriptRule
}

// New builds a Server. Recording is enabled with DefaultRecordLimit unless
// overridden by WithRecord.
func New(opts ...Option) (*Server, error) {
	s := &Server{
		clock:         clock.Real(),
		recordEnabled: true,
		recordLimit:   DefaultRecordLimit,
		models:        map[string]*modelState{},
	}
	for _, o := range opts {
		o(s)
	}
	if s.clock == nil {
		return nil, errors.New("fakellm: nil clock")
	}
	if s.recordEnabled {
		if s.recordLimit < 1 {
			return nil, fmt.Errorf("fakellm: record limit must be at least 1, got %d", s.recordLimit)
		}
		s.log = newRecorder(s.recordLimit)
	}
	return s, nil
}

// Handler returns the HTTP handler serving C2 plus /healthz and /readyz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", okHandler)
	mux.HandleFunc("GET /readyz", okHandler)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/embeddings", s.handleEmbeddings)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("PUT /_fake/script", s.handlePutScript)
	mux.HandleFunc("DELETE /_fake/script", s.handleDeleteScript)
	mux.HandleFunc("GET /_fake/requests", s.handleGetRequests)
	mux.HandleFunc("DELETE /_fake/requests", s.handleDeleteRequests)
	return mux
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// state returns the state for model, creating it; callers hold s.mu.
func (s *Server) state(model string) (*modelState, bool) {
	st, ok := s.models[model]
	if ok {
		return st, true
	}
	if len(s.models) >= maxModels {
		return nil, false
	}
	st = &modelState{}
	s.models[model] = st
	return st, true
}

// touch registers a model name as seen.
func (s *Server) touch(model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.state(model)
	return ok
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	names := make([]string, 0, len(s.models)+1)
	for name := range s.models {
		if name != DefaultModel {
			names = append(names, name)
		}
	}
	s.mu.Unlock()
	sort.Strings(names)
	names = append([]string{DefaultModel}, names...)

	created := s.clock.Now().Unix()
	type modelObj struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]modelObj, 0, len(names))
	for _, n := range names {
		data = append(data, modelObj{ID: n, Object: keyModel, Created: created, OwnedBy: "fakellm"})
	}
	writeJSON(w, http.StatusOK, map[string]any{keyObject: objectList, "data": data})
}
