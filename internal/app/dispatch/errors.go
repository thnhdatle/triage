// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

// Errors of the dispatch use case. Provider failures are returned as they are
// (wrapped in *Error), so errors.Is works with the provider sentinels.
var (
	// ErrNoTarget is returned when the plan has no target in an allowed
	// locality, or every candidate was refused.
	ErrNoTarget = errors.New("dispatch: no eligible target")
	// ErrMaskLeak refuses a cloud target whose masked request still contains
	// a finding value (privacy.Mapping.Leaks); cloud targets are then skipped
	// for the rest of the request.
	ErrMaskLeak = errors.New("dispatch: masked request still contains a finding value")
	// ErrRescan refuses a cloud target for a continuation whose partial text
	// could not be re-scanned; it wraps the detector error or
	// privacy.ErrRescanIncomplete.
	ErrRescan = errors.New("dispatch: continuation text could not be scanned")
	// ErrUnknownProvider refuses a target whose provider is not configured.
	ErrUnknownProvider = errors.New("dispatch: target provider not configured")
	// ErrAttemptTimeout ends an attempt that exceeded its attempt timeout; it
	// matches provider.ErrTimeout and is retryable.
	ErrAttemptTimeout = fmt.Errorf("dispatch: attempt timeout: %w", provider.ErrTimeout)
	// ErrTotalTimeout ends a request that exceeded dispatch.total_timeout; it
	// matches provider.ErrTimeout.
	ErrTotalTimeout = fmt.Errorf("dispatch: total timeout: %w", provider.ErrTimeout)
)

// Error is a failed dispatch: the last attempt error plus every attempt.
type Error struct {
	// Last is the error that ended the dispatch (provider error, timeout,
	// context error, refusal or a Sink error).
	Last error
	// Attempts lists every attempt in order.
	Attempts []decision.Attempt
	// Started reports that bytes were relayed to the client before the
	// failure (streams only): headers are sent, so the client got an SSE
	// error event instead of an HTTP error.
	Started bool
}

// Error implements error.
func (e *Error) Error() string {
	return "dispatch: " + strconv.Itoa(len(e.Attempts)) + " attempt(s): " + e.Last.Error()
}

// Unwrap returns the last error.
func (e *Error) Unwrap() error { return e.Last }

// isRefusal reports whether err refused a target without sending to it.
func isRefusal(err error) bool {
	return errors.Is(err, ErrMaskLeak) || errors.Is(err, ErrRescan) || errors.Is(err, ErrUnknownProvider)
}

// retryable reports whether another attempt may follow err.
func retryable(err error) bool {
	return isRefusal(err) || provider.IsRetryable(err)
}

// statusOf returns the decision status class of an attempt error.
func statusOf(err error) string {
	checks := []struct {
		target error
		status string
	}{
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "canceled"},
		{ErrMaskLeak, "mask_leak"},
		{privacy.ErrRescanIncomplete, "rescan_incomplete"},
		{ErrRescan, "rescan_error"},
		{ErrUnknownProvider, "unknown_provider"},
		{provider.ErrRateLimited, "rate_limited"},
		{provider.ErrTimeout, "timeout"},
		{provider.ErrContextLength, "context_length"},
		{provider.ErrBadRequest, "bad_request"},
		{provider.ErrUpstreamUnavailable, "unavailable"},
	}
	if err == nil {
		return statusOK
	}
	for _, c := range checks {
		if errors.Is(err, c.target) {
			return c.status
		}
	}
	if provider.StatusCode(err) >= 500 {
		return "server_error"
	}
	return "error"
}

// Attempt statuses set by dispatch itself.
const (
	statusOK  = "ok"
	statusCut = "cut"
)
