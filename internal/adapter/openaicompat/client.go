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

const (
	DefaultTimeout             = 60 * time.Second
	DefaultMaxIdleConnsPerHost = 16
	DefaultMaxResponseBytes    = 32 << 20
)

var ErrConfig = errors.New("openaicompat: invalid configuration")

type Option func(*Client)

func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

func WithExtraHeaders(h map[string]string) Option {
	return func(c *Client) { maps.Copy(c.headers, h) }
}

func WithModelHeaders(model string, h map[string]string) Option {
	return func(c *Client) {
		m := make(map[string]string, len(h))
		maps.Copy(m, h)
		c.modelHeaders[model] = m
	}
}

func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

func WithMaxIdleConnsPerHost(n int) Option { return func(c *Client) { c.maxIdle = n } }

func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

func WithMaxResponseBytes(n int64) Option { return func(c *Client) { c.maxBytes = n } }

func WithClock(clk clock.Clock) Option { return func(c *Client) { c.clk = clk } }

// Client is a provider for an OpenAI-compatible backend.
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

// New returns a Client for the backend at baseURL.
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

func (c *Client) CloseIdleConnections() { c.hc.CloseIdleConnections() }
