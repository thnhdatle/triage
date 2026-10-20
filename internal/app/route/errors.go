// SPDX-License-Identifier: Apache-2.0

package route

import (
	"context"
	"errors"
	"net/http"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// Errors of the routing pipeline. Privacy failures are returned as the
// privacy sentinels (privacy.ErrModeNotAllowed, privacy.ErrScanLimit,
// privacy.ErrNoLocalTarget), wrapped with context; dispatch failures as the
// *dispatch.Error returned by the dispatcher.
var (
	// ErrUnknownRoute is returned when the model field names no route
	// (404 model_not_found).
	ErrUnknownRoute = errors.New("route: unknown model")
	// ErrNoEligibleTarget is returned when no target passes selection and the
	// request is not restricted to local targets (503 no_eligible_target).
	ErrNoEligibleTarget = errors.New("route: no eligible target")
)

// Client-facing error codes (docs/guides/errors.md).
const (
	CodeModelNotFound         = "model_not_found"
	CodePrivacyModeNotAllowed = "privacy_mode_not_allowed"
	CodePrivacyScanLimit      = "privacy_scan_limit"
	CodePrivacyNoLocalTarget  = "privacy_no_local_target"
	CodeNoEligibleTarget      = "no_eligible_target"
	CodeContextLength         = "context_length_exceeded"
	CodeUpstreamBadRequest    = "upstream_bad_request"
	CodeRateLimited           = "rate_limit_exceeded"
	CodeUpstreamTimeout       = "upstream_timeout"
	CodeUpstreamUnavailable   = "upstream_unavailable"
	CodeRequestCanceled       = "request_canceled"
	CodeInternal              = "internal_error"
)

// StatusClientClosedRequest is the non-standard status used for a request
// the client abandoned.
const StatusClientClosedRequest = 499

// codeTable is checked in order; privacy sentinels come first because a
// dispatch failure may wrap them.
var codeTable = []struct {
	err    error
	code   string
	status int
}{
	{ErrUnknownRoute, CodeModelNotFound, http.StatusNotFound},
	{privacy.ErrModeNotAllowed, CodePrivacyModeNotAllowed, http.StatusBadRequest},
	{privacy.ErrScanLimit, CodePrivacyScanLimit, http.StatusRequestEntityTooLarge},
	{privacy.ErrNoLocalTarget, CodePrivacyNoLocalTarget, http.StatusServiceUnavailable},
	{ErrNoEligibleTarget, CodeNoEligibleTarget, http.StatusServiceUnavailable},
	{dispatch.ErrNoTarget, CodeNoEligibleTarget, http.StatusServiceUnavailable},
	{dispatch.ErrMaskLeak, CodeNoEligibleTarget, http.StatusServiceUnavailable},
	{dispatch.ErrRescan, CodeNoEligibleTarget, http.StatusServiceUnavailable},
	{dispatch.ErrUnknownProvider, CodeNoEligibleTarget, http.StatusServiceUnavailable},
	{context.Canceled, CodeRequestCanceled, StatusClientClosedRequest},
	{provider.ErrContextLength, CodeContextLength, http.StatusBadRequest},
	{provider.ErrBadRequest, CodeUpstreamBadRequest, http.StatusBadRequest},
	{provider.ErrRateLimited, CodeRateLimited, http.StatusTooManyRequests},
	{provider.ErrTimeout, CodeUpstreamTimeout, http.StatusGatewayTimeout},
	{context.DeadlineExceeded, CodeUpstreamTimeout, http.StatusGatewayTimeout},
	{provider.ErrUpstreamUnavailable, CodeUpstreamUnavailable, http.StatusBadGateway},
}

// Code maps an error returned by Pipeline to its client-facing code and HTTP
// status. The pipeline's own and the privacy codes are fixed (docs/guides/errors.md);
// the upstream codes (rate limit, timeout, unavailable, bad request, context
// length) are the provisional registry entries httpapi uses. nil maps to
// ("", 200); an unknown error to internal_error 500.
func Code(err error) (code string, status int) {
	if err == nil {
		return "", http.StatusOK
	}
	for _, c := range codeTable {
		if errors.Is(err, c.err) {
			return c.code, c.status
		}
	}
	if s := provider.StatusCode(err); s >= 500 {
		return CodeUpstreamUnavailable, http.StatusBadGateway
	}
	return CodeInternal, http.StatusInternalServerError
}
