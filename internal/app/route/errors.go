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

var (
	ErrUnknownRoute     = errors.New("route: unknown model")
	ErrNoEligibleTarget = errors.New("route: no eligible target")
)

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

const StatusClientClosedRequest = 499

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

// Code returns the OpenAI error code and HTTP status for err.
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
