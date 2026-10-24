// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
)

type ctxKey int

const requestIDKey ctxKey = 1

const HeaderRequestID = "X-Request-Id"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func (s *Server) chain(h http.Handler) http.Handler {
	h = s.accessLogger(h)
	for i := len(s.wrap) - 1; i >= 0; i-- {
		h = s.wrap[i](h)
	}
	return s.recoverer(s.withRequestID(h))
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !validRequestID.MatchString(id) {
			id = s.newID()
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.handlePanic(w, r, rec)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handlePanic(w http.ResponseWriter, r *http.Request, rec any) {
	if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(rec)
	}
	s.log.ErrorContext(r.Context(), "panic in handler", "request_id", requestID(r.Context()), "error", rec,
		"stack", string(debug.Stack()))
	writeAPIError(w, internalError())
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	return n, err //nolint:wrapcheck // transparent writer
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) accessLogger(next http.Handler) http.Handler {
	if !s.accessLog {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clk.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.log.Info("request", "request_id", requestID(r.Context()), "operation", r.Method+" "+r.URL.Path,
			"status", sw.status, "duration_ms", s.clk.Now().Sub(start).Milliseconds())
	})
}

func method(m string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != m {
			w.Header().Set("Allow", m)
			writeAPIError(w, &apiError{
				status: http.StatusMethodNotAllowed, code: CodeMethodNotAllowed,
				message: "method " + r.Method + " is not allowed on " + r.URL.Path,
			})
			return
		}
		h.ServeHTTP(w, r)
	})
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, &apiError{status: http.StatusNotFound, code: CodeNotFound, message: "no such endpoint " + r.URL.Path})
}

func (s *Server) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !s.auth.Authenticate(strings.TrimSpace(token)) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeAPIError(w, &apiError{
					status: http.StatusUnauthorized, code: CodeInvalidAPIKey,
					message: "missing or invalid API key; send Authorization: Bearer <key>",
				})
				return
			}
		}
		h(w, r)
	})
}

type KeyAuth struct {
	digests [][sha256.Size]byte
}

func NewKeyAuth(keys []string) *KeyAuth {
	a := &KeyAuth{}
	for _, k := range keys {
		if k != "" {
			a.digests = append(a.digests, sha256.Sum256([]byte(k)))
		}
	}
	return a
}

func (a *KeyAuth) Authenticate(token string) bool {
	d := sha256.Sum256([]byte(token))
	ok := 0
	for _, k := range a.digests {
		ok |= subtle.ConstantTimeCompare(d[:], k[:])
	}
	return ok == 1
}
