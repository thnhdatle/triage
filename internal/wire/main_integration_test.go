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

var container *fakellmcontainer.Container

func TestMain(m *testing.M) {
	os.Exit(fakellmcontainer.RunTests(m, func(c *fakellmcontainer.Container) { container = c }))
}

func containerFake(t *testing.T) *fake {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &fake{url: container.URL(), hc: &http.Client{Transport: tr, Timeout: 10 * time.Second}}
}
