// SPDX-License-Identifier: Apache-2.0

package openaicompat

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

type errorBody struct {
	Error *struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	} `json:"error"`
}

type errorInfo struct {
	typ, code, message string
}

func parseErrorBody(data []byte) errorInfo {
	var b errorBody
	if json.Unmarshal(data, &b) != nil || b.Error == nil {
		return errorInfo{}
	}
	info := errorInfo{typ: b.Error.Type, message: b.Error.Message}
	var s string
	if json.Unmarshal(b.Error.Code, &s) == nil {
		info.code = s
	} else if len(b.Error.Code) > 0 && string(b.Error.Code) != jsonNull {
		info.code = string(b.Error.Code)
	}
	return info
}

func (i errorInfo) describe(status int) string {
	s := "upstream status " + strconv.Itoa(status)
	if i.typ != "" {
		s += " type " + i.typ
	}
	if i.code != "" {
		s += " code " + i.code
	}
	return s
}

var contextLengthMarkers = []string{
	"context length", "context_length", "context window", "maximum context",
	"too many tokens", "prompt is too long", "exceeds the context",
}

func (i errorInfo) isContextLength() bool {
	if i.code == "context_length_exceeded" || i.typ == "context_length_exceeded" {
		return true
	}
	msg := strings.ToLower(i.message)
	for _, m := range contextLengthMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

func (c *Client) statusError(resp *http.Response, body []byte) error {
	info := parseErrorBody(body)
	e := &provider.Error{StatusCode: resp.StatusCode, Message: info.describe(resp.StatusCode)}
	switch st := resp.StatusCode; {
	case st == http.StatusTooManyRequests:
		e.Kind = provider.ErrRateLimited
		e.RetryAfter = retryAfter(resp.Header, c.clk.Now())
	case st == http.StatusRequestTimeout || st == http.StatusGatewayTimeout:
		e.Kind = provider.ErrTimeout
	case st >= 500:
		e.Kind = provider.ErrUpstreamUnavailable
	case info.isContextLength():
		e.Kind = provider.ErrContextLength
	default:
		e.Kind = provider.ErrBadRequest
	}
	return e
}

func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		if s <= 0 {
			return 0
		}
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &provider.Error{Kind: provider.ErrTimeout, Message: "deadline exceeded"}
		}
		return context.Cause(ctx) //nolint:wrapcheck // the caller's cancellation is returned as is
	}
	return netError(err)
}

func netError(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &provider.Error{Kind: provider.ErrTimeout, Message: "network timeout"}
	}
	return &provider.Error{Kind: provider.ErrUpstreamUnavailable, Message: "transport error: " + err.Error()}
}

func malformed(status int, what string) error {
	return &provider.Error{
		Kind: provider.ErrUpstreamUnavailable, StatusCode: status, Message: "malformed upstream response: " + what,
	}
}

func badRequest(what string) error {
	return &provider.Error{Kind: provider.ErrBadRequest, Message: "openaicompat: " + what}
}
