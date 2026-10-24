// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"net/http"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, aerr := readBody(w, r, s.current().MaxBodyBytes)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	req, aerr := decodeChat(body)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	req.Headers = requestHeaders(r.Header)
	req.ID = s.newID()
	if req.Stream {
		s.streamChat(w, r, req)
		return
	}
	out, err := s.router.Chat(route.WithDecisionID(r.Context(), req.ID), req)
	s.setOutcomeHeaders(w, out)
	if err != nil {
		writeAPIError(w, fromError(err))
		return
	}
	modelName := orElse(out.Model, out.Response.Model)
	writeJSON(w, http.StatusOK, encodeResponse(out.Response, "chatcmpl-"+orElse(out.DecisionID, req.ID), modelName,
		s.clk.Now().Unix()))
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req chat.Request) {
	sink := newSSESink(s, w, req)
	out, err := s.router.ChatStream(route.WithDecisionID(r.Context(), req.ID), req, sink)
	sink.finish(out, err)
}

func (s *Server) routeError(w http.ResponseWriter, out route.Outcome, err error) {
	s.setOutcomeHeaders(w, out)
	writeAPIError(w, fromError(err))
}
