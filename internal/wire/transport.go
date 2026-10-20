// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel/propagation"
)

// propagatingTransport injects the W3C trace context of the request's
// context (traceparent, tracestate) into every outgoing provider request.
type propagatingTransport struct {
	next *http.Transport
}

// RoundTrip implements http.RoundTripper.
func (t propagatingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	carrier := propagation.HeaderCarrier(http.Header{})
	propagation.TraceContext{}.Inject(r.Context(), carrier)
	if len(carrier) > 0 {
		r = r.Clone(r.Context())
		for k, v := range carrier {
			r.Header[k] = v
		}
	}
	return t.next.RoundTrip(r) //nolint:wrapcheck // a transport must return the inner error unchanged
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the pool.
func (t propagatingTransport) CloseIdleConnections() { t.next.CloseIdleConnections() }

// newHTTPClient returns a provider HTTP client with its own connection pool.
// maxIdle <= 0 keeps the adapters' default pool size.
func newHTTPClient(maxIdle int, headerTimeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // stdlib default is *http.Transport
	if maxIdle > 0 {
		tr.MaxIdleConnsPerHost = maxIdle
	} else {
		tr.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}
	tr.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: propagatingTransport{next: tr}}
}

// defaultMaxIdleConnsPerHost matches the provider adapters' default.
const defaultMaxIdleConnsPerHost = 16
