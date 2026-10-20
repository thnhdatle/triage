// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/thnhdatle/triage/internal/app/route"
)

// Codes produced by this package itself; the rest come from route.Code.
// All are registered in docs/guides/errors.md.
const (
	CodeInvalidRequest   = "invalid_request"
	CodeInvalidAPIKey    = "invalid_api_key"
	CodeRequestTooLarge  = "request_too_large"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeInvalidCursor    = "invalid_cursor"
	CodeDecisionNotFound = "decision_not_found"
	CodeConfigInvalid    = "config_invalid"
	CodeInternal         = route.CodeInternal
)

const (
	maxMessageLen = 1000
	paramModel    = "model"
)

// apiError is a client-facing error in OpenAI shape with its HTTP status.
type apiError struct {
	status  int
	code    string
	param   string
	message string
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

func (e *apiError) body() errorEnvelope {
	b := errorBody{Message: e.message, Type: errorType(e.status), Code: e.code}
	if e.param != "" {
		p := e.param
		b.Param = &p
	}
	return errorEnvelope{Error: b}
}

func errorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= http.StatusInternalServerError:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

func internalError() *apiError {
	return &apiError{status: http.StatusInternalServerError, code: CodeInternal, message: "internal error"}
}

func invalid(param, msg string) *apiError {
	return &apiError{status: http.StatusBadRequest, code: CodeInvalidRequest, param: param, message: msg}
}

// fromError maps a pipeline error to an apiError through route.Code. The
// message of internal errors is generic so nothing internal leaks.
func fromError(err error) *apiError {
	code, status := route.Code(err)
	e := &apiError{status: status, code: code, message: truncate(err.Error())}
	if code == route.CodeInternal {
		e.message = "internal error"
	}
	if errors.Is(err, route.ErrUnknownRoute) {
		e.param = paramModel
	}
	return e
}

func truncate(s string) string {
	if len(s) > maxMessageLen {
		return s[:maxMessageLen]
	}
	return s
}

func writeAPIError(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.status, e.body())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b = []byte(`{"error":{"message":"internal error","type":"server_error","param":null,"code":"internal_error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n')) // a failed write means the client is gone
}
