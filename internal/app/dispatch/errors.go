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

var (
	ErrNoTarget        = errors.New("dispatch: no eligible target")
	ErrMaskLeak        = errors.New("dispatch: masked request still contains a finding value")
	ErrRescan          = errors.New("dispatch: continuation text could not be scanned")
	ErrUnknownProvider = errors.New("dispatch: target provider not configured")
	ErrAttemptTimeout  = fmt.Errorf("dispatch: attempt timeout: %w", provider.ErrTimeout)
	ErrTotalTimeout    = fmt.Errorf("dispatch: total timeout: %w", provider.ErrTimeout)
)

type Error struct {
	Last     error
	Attempts []decision.Attempt
	Started  bool
}

func (e *Error) Error() string {
	return "dispatch: " + strconv.Itoa(len(e.Attempts)) + " attempt(s): " + e.Last.Error()
}

func (e *Error) Unwrap() error { return e.Last }

func isRefusal(err error) bool {
	return errors.Is(err, ErrMaskLeak) || errors.Is(err, ErrRescan) || errors.Is(err, ErrUnknownProvider)
}

func retryable(err error) bool {
	return isRefusal(err) || provider.IsRetryable(err)
}

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

const (
	statusOK  = "ok"
	statusCut = "cut"
)
