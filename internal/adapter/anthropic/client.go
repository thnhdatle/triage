// SPDX-License-Identifier: Apache-2.0

package anthropic

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
	DefaultBaseURL             = "https://api.anthropic.com/v1"
	DefaultVersion             = "2023-06-01"
	DefaultMaxTokens           = 4096
	DefaultTimeout             = 60 * time.Second
	DefaultMaxIdleConnsPerHost = 16
	DefaultMaxResponseBytes    = 32 << 20
)

var ErrConfig = errors.New("anthropic: invalid configuration")

type Option func(*Client)

func WithBaseURL(u string) Option { return func(c *Client) { c.base = u } }

func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

func WithVersion(v string) Option { return func(c *Client) { c.version = v } }

func WithDefaultMaxTokens(n int) Option { return func(c *Client) { c.defaultMaxTokens = n } }

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

func WithMaxResponseBytes(n int) Option { return func(c *Client) { c.maxBytes = n } }

func WithClock(clk clock.Clock) Option { return func(c *Client) { c.clk = clk } }

// Client is a provider for the Anthropic Messages API.
type Client struct {
	base             string
	apiKey           string
	version          string
	defaultMaxTokens int
	headers          map[string]string
	modelHeaders     map[string]map[string]string
	hc               *http.Client
	maxIdle          int
	timeout          time.Duration
	maxBytes         int
	clk              clock.Clock
}

// New returns a Client.
func New(opts ...Option) (*Client, error) {
	c := &Client{
		base:             DefaultBaseURL,
		version:          DefaultVersion,
		defaultMaxTokens: DefaultMaxTokens,
		headers:          map[string]string{},
		modelHeaders:     map[string]map[string]string{},
		maxIdle:          DefaultMaxIdleConnsPerHost,
		timeout:          DefaultTimeout,
		maxBytes:         DefaultMaxResponseBytes,
		clk:              clock.Real(),
	}
	for _, o := range opts {
		o(c)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	c.base = strings.TrimRight(c.base, "/")
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

func (c *Client) validate() error {
	u, err := url.Parse(c.base)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return fmt.Errorf("%w: base url %q must be an absolute http(s) URL", ErrConfig, c.base)
	case c.version == "":
		return fmt.Errorf("%w: empty anthropic-version", ErrConfig)
	case c.defaultMaxTokens < 1:
		return fmt.Errorf("%w: default max tokens must be positive", ErrConfig)
	case c.timeout <= 0:
		return fmt.Errorf("%w: timeout must be positive", ErrConfig)
	case c.maxIdle < 0:
		return fmt.Errorf("%w: max_idle_conns_per_host must not be negative", ErrConfig)
	case c.maxBytes <= 0:
		return fmt.Errorf("%w: max response bytes must be positive", ErrConfig)
	case c.clk == nil:
		return fmt.Errorf("%w: nil clock", ErrConfig)
	}
	return nil
}

func (c *Client) CloseIdleConnections() { c.hc.CloseIdleConnections() }

func (c *Client) do(ctx context.Context, method, path, model string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	h := req.Header
	if body != nil {
		h.Set("Content-Type", "application/json")
	}
	h.Set("Anthropic-Version", c.version)
	for k, v := range c.headers {
		h.Set(k, v)
	}
	for k, v := range c.modelHeaders[model] {
		h.Set(k, v)
	}
	if c.apiKey != "" {
		h.Set("X-Api-Key", c.apiKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, c.httpError(resp, data)
	}
	return resp, nil
}

func (c *Client) fetch(ctx context.Context, method, path, model string, body []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.do(ctx, method, path, model, body)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.maxBytes)+1))
	if err != nil {
		return nil, resp.StatusCode, requestError(ctx, err)
	}
	if len(data) > c.maxBytes {
		return nil, resp.StatusCode, badUpstream(resp.StatusCode, "response body too large")
	}
	return data, resp.StatusCode, nil
}
