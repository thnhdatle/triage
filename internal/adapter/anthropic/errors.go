// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thnhdatle/triage/internal/domain/provider"
)

const maxErrorBody = 64 << 10

type apiError struct {
	typ     string
	message string
}

func parseAPIError(data []byte) apiError {
	var b struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &b) != nil || b.Error == nil {
		return apiError{}
	}
	return apiError{typ: b.Error.Type, message: b.Error.Message}
}

func (a apiError) describe(status int) string {
	s := "upstream status " + strconv.Itoa(status)
	if a.typ != "" {
		s += " type " + a.typ
	}
	return s
}

func (a apiError) promptTooLong() bool {
	msg := strings.ToLower(a.message)
	return strings.Contains(msg, "prompt is too long") || strings.Contains(msg, "context window") ||
		strings.Contains(msg, "context length")
}

func (a apiError) kind() error {
	switch a.typ {
	case "rate_limit_error":
		return provider.ErrRateLimited
	case "timeout_error":
		return provider.ErrTimeout
	case "invalid_request_error", "authentication_error", "permission_error", "not_found_error",
		"request_too_large", "billing_error":
		if a.promptTooLong() {
			return provider.ErrContextLength
		}
		return provider.ErrBadRequest
	}
	return provider.ErrUpstreamUnavailable
}

func (c *Client) httpError(resp *http.Response, body []byte) error {
	a := parseAPIError(body)
	e := &provider.Error{StatusCode: resp.StatusCode, Message: a.describe(resp.StatusCode)}
	switch st := resp.StatusCode; {
	case st == http.StatusTooManyRequests:
		e.Kind = provider.ErrRateLimited
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), c.clk.Now())
	case st == http.StatusRequestTimeout || st == http.StatusGatewayTimeout:
		e.Kind = provider.ErrTimeout
	case st >= 500:
		e.Kind = provider.ErrUpstreamUnavailable
	case a.promptTooLong():
		e.Kind = provider.ErrContextLength
	default:
		e.Kind = provider.ErrBadRequest
	}
	return e
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		return max(0, time.Duration(s*float64(time.Second)))
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

func requestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &provider.Error{Kind: provider.ErrTimeout, Message: "deadline exceeded"}
		}
		return context.Cause(ctx) //nolint:wrapcheck // the caller's cancellation is returned as is
	}
	return connError(err)
}

func connError(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &provider.Error{Kind: provider.ErrTimeout, Message: "network timeout"}
	}
	return &provider.Error{Kind: provider.ErrUpstreamUnavailable, Message: "transport error: " + err.Error()}
}

func badUpstream(status int, what string) error {
	return &provider.Error{
		Kind: provider.ErrUpstreamUnavailable, StatusCode: status, Message: "malformed upstream response: " + what,
	}
}

func unsupported(what string) error {
	return &provider.Error{Kind: provider.ErrBadRequest, Message: "anthropic: " + what}
}
