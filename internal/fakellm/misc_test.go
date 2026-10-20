// SPDX-License-Identifier: Apache-2.0

package fakellm_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

var errEOF = io.EOF

func withCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(t.Context())
}

func jsonValid(s string) error {
	var v any
	return json.Unmarshal([]byte(s), &v)
}
