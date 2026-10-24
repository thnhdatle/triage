// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"net/http"
	"sync"
)

const (
	KindChat       = "chat"
	KindEmbeddings = "embeddings"
)

type Record struct {
	Method     string      `json:"method"`
	Path       string      `json:"path"`
	Headers    http.Header `json:"headers"`
	Body       string      `json:"body"`
	Model      string      `json:"model"`
	Kind       string      `json:"kind"`
	InputCount int         `json:"input_count"`
}

type recorder struct {
	mu      sync.Mutex
	buf     []Record
	start   int
	dropped int
	limit   int
}

func newRecorder(limit int) *recorder { return &recorder{limit: limit} }

func (r *recorder) add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) < r.limit {
		r.buf = append(r.buf, rec)
		return
	}
	r.buf[r.start] = rec
	r.start = (r.start + 1) % r.limit
	r.dropped++
}

func (r *recorder) snapshot() ([]Record, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(r.buf))
	out = append(out, r.buf[r.start:]...)
	out = append(out, r.buf[:r.start]...)
	return out, r.dropped
}

func (r *recorder) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = nil
	r.start = 0
	r.dropped = 0
}

func (s *Server) record(r *http.Request, body []byte, model, kind string, inputs int) {
	if s.log == nil {
		return
	}
	s.log.add(Record{
		Method: r.Method, Path: r.URL.Path, Headers: r.Header.Clone(),
		Body: string(body), Model: model, Kind: kind, InputCount: inputs,
	})
}

func (s *Server) handleGetRequests(w http.ResponseWriter, _ *http.Request) {
	recs, dropped := []Record{}, 0
	if s.log != nil {
		recs, dropped = s.log.snapshot()
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": recs, "dropped": dropped})
}

func (s *Server) handleDeleteRequests(w http.ResponseWriter, _ *http.Request) {
	if s.log != nil {
		s.log.clear()
	}
	w.WriteHeader(http.StatusNoContent)
}
