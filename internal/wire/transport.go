// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel/propagation"
)

type propagatingTransport struct {
	next *http.Transport
}

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

func (t propagatingTransport) CloseIdleConnections() { t.next.CloseIdleConnections() }

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

const defaultMaxIdleConnsPerHost = 16
