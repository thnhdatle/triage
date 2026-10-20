// SPDX-License-Identifier: Apache-2.0

//go:build integration

package wire_test

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/thnhdatle/triage/internal/testutil/fakellmcontainer"
)

// container is the fakellm container shared by the integration tests of this
// package. Each test isolates itself by upstream model names (see
// containerFake) and selects scenarios with per-target X-Fake-* headers, so
// tests run in parallel without touching the global script.
var container *fakellmcontainer.Container

func TestMain(m *testing.M) {
	os.Exit(fakellmcontainer.RunTests(m, func(c *fakellmcontainer.Container) { container = c }))
}

// containerFake returns the harness view of the fakellm container.
func containerFake(t *testing.T) *fake {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &fake{url: container.URL(), hc: &http.Client{Transport: tr, Timeout: 10 * time.Second}}
}
