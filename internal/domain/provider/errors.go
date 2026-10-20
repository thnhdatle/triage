// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"errors"
	"time"
)

// Normalised provider errors. Adapters return an *Error wrapping one
// of these, so callers use errors.Is.
var (
	ErrRateLimited         = errors.New("provider: rate limited")
	ErrUpstreamUnavailable = errors.New("provider: upstream unavailable")
	ErrBadRequest          = errors.New("provider: bad request")
	ErrTimeout             = errors.New("provider: timeout")
	ErrContextLength       = errors.New("provider: context length exceeded")
)

// Error is an upstream failure with its HTTP status and Retry-After.
type Error struct {
	// Kind is one of the sentinel errors above.
	Kind error
	// StatusCode is the upstream HTTP status, 0 when none was received.
	StatusCode int
	// RetryAfter is the upstream Retry-After, 0 when absent.
	RetryAfter time.Duration
	// Message is the upstream error message, never containing request content.
	Message string
}

// Error implements error.
func (e *Error) Error() string {
	s := e.Kind.Error()
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Unwrap returns the sentinel kind.
func (e *Error) Unwrap() error { return e.Kind }

// RetryAfter returns the Retry-After carried by err, or 0.
func RetryAfter(err error) time.Duration {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.RetryAfter
	}
	return 0
}

// StatusCode returns the upstream HTTP status carried by err, or 0.
func StatusCode(err error) int {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.StatusCode
	}
	return 0
}

// IsRetryable reports whether a fallback may try another target after err:
// rate limit, unavailable, timeout, or a 5xx status.
func IsRetryable(err error) bool {
	switch {
	case errors.Is(err, ErrBadRequest), errors.Is(err, ErrContextLength):
		return false
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrUpstreamUnavailable), errors.Is(err, ErrTimeout):
		return true
	}
	return StatusCode(err) >= 500
}
