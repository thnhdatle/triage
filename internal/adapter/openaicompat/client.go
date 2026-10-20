// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thnhdatle/triage/internal/platform/clock"
)

// Defaults of a Client.
const (
	// DefaultTimeout bounds a non-streaming call and the wait for a stream's
	// response headers.
	DefaultTimeout = 60 * time.Second
	// DefaultMaxIdleConnsPerHost is the idle connection pool size per host.
	DefaultMaxIdleConnsPerHost = 16
	// DefaultMaxResponseBytes bounds a non-streaming response body and one
	// stream event.
	DefaultMaxResponseBytes = 32 << 20
)

// ErrConfig is returned by New for an invalid configuration.
var ErrConfig = errors.New("openaicompat: invalid configuration")

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sends key as a bearer token; empty sends no Authorization header.
func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

// WithExtraHeaders adds headers to every request (provider-wide).
func WithExtraHeaders(h map[string]string) Option {
	return func(c *Client) { maps.Copy(c.headers, h) }
}

// WithModelHeaders adds headers to requests whose model is model (per-target
// extra_headers). They override provider-wide headers.
func WithModelHeaders(model string, h map[string]string) Option {
	return func(c *Client) {
		m := make(map[string]string, len(h))
		maps.Copy(m, h)
		c.modelHeaders[model] = m
	}
}

// WithHTTPClient replaces the HTTP client; WithMaxIdleConnsPerHost and the
// header timeout then have no effect.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithMaxIdleConnsPerHost sets the idle connection pool size per host.
func WithMaxIdleConnsPerHost(n int) Option { return func(c *Client) { c.maxIdle = n } }

// WithTimeout bounds non-streaming calls and the wait for stream headers.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithMaxResponseBytes bounds a response body and one stream event.
func WithMaxResponseBytes(n int64) Option { return func(c *Client) { c.maxBytes = n } }

// WithClock sets the clock used to interpret HTTP-date Retry-After values.
func WithClock(clk clock.Clock) Option { return func(c *Client) { c.clk = clk } }

// Client is an OpenAI-compatible provider. It is safe for concurrent use.
type Client struct {
	base         string
	apiKey       string
	headers      map[string]string
	modelHeaders map[string]map[string]string
	hc           *http.Client
	maxIdle      int
	timeout      time.Duration
	maxBytes     int64
	clk          clock.Clock
}

// New returns a Client for baseURL (e.g. "http://localhost:1234/v1").
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: base url %q must be an absolute http(s) URL", ErrConfig, baseURL)
	}
	c := &Client{
		base:         strings.TrimRight(baseURL, "/"),
		headers:      map[string]string{},
		modelHeaders: map[string]map[string]string{},
		maxIdle:      DefaultMaxIdleConnsPerHost,
		timeout:      DefaultTimeout,
		maxBytes:     DefaultMaxResponseBytes,
		clk:          clock.Real(),
	}
	for _, o := range opts {
		o(c)
	}
	switch {
	case c.timeout <= 0:
		return nil, fmt.Errorf("%w: timeout must be positive", ErrConfig)
	case c.maxIdle < 0:
		return nil, fmt.Errorf("%w: max_idle_conns_per_host must not be negative", ErrConfig)
	case c.maxBytes <= 0:
		return nil, fmt.Errorf("%w: max response bytes must be positive", ErrConfig)
	case c.clk == nil:
		return nil, fmt.Errorf("%w: nil clock", ErrConfig)
	}
	if c.hc == nil {
		tr, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("%w: default transport is not *http.Transport", ErrConfig)
		}
		tr = tr.Clone()
		tr.MaxIdleConnsPerHost = c.maxIdle
		tr.ResponseHeaderTimeout = c.timeout
		c.hc = &http.Client{Transport: tr}
	}
	return c, nil
}

// send performs one request and returns the response when its status is 2xx;
// any failure is already normalised. The caller closes the body.
func (c *Client) send(ctx context.Context, method, path, model string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.modelHeaders[model] {
		req.Header.Set(k, v)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, c.statusError(resp, data)
	}
	return resp, nil
}

// call performs a bounded non-streaming request and returns the body.
func (c *Client) call(ctx context.Context, method, path, model string, body []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.send(ctx, method, path, model, body)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, resp.StatusCode, transportError(ctx, err)
	}
	if int64(len(data)) > c.maxBytes {
		return nil, resp.StatusCode, malformed(resp.StatusCode, "response body too large")
	}
	return data, resp.StatusCode, nil
}

// CloseIdleConnections closes idle pooled connections (shutdown, tests).
func (c *Client) CloseIdleConnections() { c.hc.CloseIdleConnections() }
