// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"net/http"
	"strings"

	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/decision"
)

// Response headers of F1 and F8.
const (
	HeaderDecision    = "X-Triage-Decision"
	HeaderRoute       = "X-Triage-Route"
	HeaderTarget      = "X-Triage-Target"
	HeaderRouteReason = "X-Triage-Route-Reason"
	// HeaderUnmask is "partial" on a non-streamed response whose output kept a
	// placeholder-like token that is not a mapping key (F5); streams signal it
	// with an SSE comment instead.
	HeaderUnmask = "X-Triage-Unmask"
)

// notForwarded are request headers that describe the client connection or
// carry the client's credentials; every other header travels with the request
// as chat.Request.Headers (C1: extra headers are forwarded, X-Triage-* control
// headers are read by the route pipeline).
var notForwarded = map[string]bool{
	"Authorization": true, "Cookie": true, "Host": true, "Content-Length": true, "Content-Type": true,
	"Accept": true, "Accept-Encoding": true, "Accept-Language": true, "Connection": true, "Keep-Alive": true,
	"Transfer-Encoding": true, "Te": true, "Trailer": true, "Upgrade": true, "User-Agent": true,
	"Proxy-Authorization": true, "Proxy-Connection": true, "Expect": true, "Origin": true, "Referer": true,
	"Traceparent": true, "Tracestate": true, "Baggage": true, HeaderRequestID: true,
}

func requestHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if len(v) == 0 || notForwarded[k] || strings.HasPrefix(k, "Sec-") {
			continue
		}
		out[k] = v[0]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// setOutcomeHeaders sets the X-Triage-* headers known from an outcome.
func (s *Server) setOutcomeHeaders(w http.ResponseWriter, out route.Outcome) {
	h := w.Header()
	if out.DecisionID != "" {
		h.Set(HeaderDecision, out.DecisionID)
	}
	if out.Route != "" {
		h.Set(HeaderRoute, out.Route)
	}
	if out.Target != "" {
		h.Set(HeaderTarget, out.Target)
	}
	if out.Decision.Unmask == decision.UnmaskPartial {
		h.Set(HeaderUnmask, string(decision.UnmaskPartial))
	}
	if s.current().ExposeDecisionDetail && len(out.Decision.Reasons) > 0 {
		h.Set(HeaderRouteReason, asciiHeader(strings.Join(out.Decision.Reasons, "; ")))
	}
}

func asciiHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}
