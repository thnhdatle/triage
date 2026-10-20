// SPDX-License-Identifier: Apache-2.0

package loadgen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// NewClient returns an HTTP client sized for open-model load: keep-alive
// connections up to maxConns per host (they are reused, never queued behind a
// small idle pool) and no transparent compression.
func NewClient(maxConns int, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        maxConns,
		MaxIdleConnsPerHost: maxConns,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// Post sends body to url with the given headers (name/value pairs), drains
// the response and returns an error unless the status is 200.
func Post(ctx context.Context, hc *http.Client, url string, body []byte, headers ...string) error {
	return PostCheck(ctx, hc, url, body, nil, headers...)
}

// PostCheck is Post with check (when non-nil) applied to the headers of a
// 200 response; its error becomes the request's error.
func PostCheck(
	ctx context.Context, hc *http.Client, url string, body []byte, check func(http.Header) error, headers ...string,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("loadgen: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("loadgen: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("loadgen: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("loadgen: status %d", resp.StatusCode)
	}
	if check != nil {
		return check(resp.Header)
	}
	return nil
}
