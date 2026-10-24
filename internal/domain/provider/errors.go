// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"errors"
	"time"
)

var (
	ErrRateLimited         = errors.New("provider: rate limited")
	ErrUpstreamUnavailable = errors.New("provider: upstream unavailable")
	ErrBadRequest          = errors.New("provider: bad request")
	ErrTimeout             = errors.New("provider: timeout")
	ErrContextLength       = errors.New("provider: context length exceeded")
)

type Error struct {
	Kind       error
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *Error) Error() string {
	s := e.Kind.Error()
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *Error) Unwrap() error { return e.Kind }

func RetryAfter(err error) time.Duration {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.RetryAfter
	}
	return 0
}

func StatusCode(err error) int {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.StatusCode
	}
	return 0
}

func IsRetryable(err error) bool {
	switch {
	case errors.Is(err, ErrBadRequest), errors.Is(err, ErrContextLength):
		return false
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrUpstreamUnavailable), errors.Is(err, ErrTimeout):
		return true
	}
	return StatusCode(err) >= 500
}
