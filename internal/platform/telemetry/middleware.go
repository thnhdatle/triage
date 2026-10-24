// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	RouteSpanName = "route request"
	unmatched     = "unmatched"
)

// Middleware counts requests, observes their duration and opens the request span.
func (t *Telemetry) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			t.m.httpInflight.Inc()
			defer t.m.httpInflight.Dec()

			ctx := r.Context()
			var span trace.Span
			if !untraced(r.URL.Path) {
				ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(r.Header))
				ctx, span = t.tracer.Start(ctx, RouteSpanName, trace.WithSpanKind(trace.SpanKindServer))
			}
			r = r.WithContext(ctx)
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)

			status := sw.code()
			path := unmatched
			if r.Pattern != "" {
				path = r.Pattern
			}
			method := methodLabel(r.Method)
			t.m.httpRequests.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
			t.m.httpDuration.WithLabelValues(method, path).Observe(time.Since(start).Seconds())
			if span != nil {
				span.SetAttributes(
					attribute.String("http.request.method", method),
					attribute.String("http.route", path),
					attribute.Int("http.response.status_code", status),
				)
				if status >= http.StatusInternalServerError {
					span.SetStatus(codes.Error, http.StatusText(status))
				}
				span.End()
			}
		})
	}
}

func untraced(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	return false
}

func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodPatch, http.MethodHead, http.MethodOptions:
		return strings.ToUpper(m)
	}
	return otherLabel
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *statusWriter) WriteHeader(c int) {
	if w.status == 0 && (c >= 200 || c == http.StatusSwitchingProtocols) {
		w.status = c
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
