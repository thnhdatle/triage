// SPDX-License-Identifier: Apache-2.0

//go:build integration

package openaicompat_test

import (
	"os"
	"testing"

	"github.com/thnhdatle/triage/internal/testutil/fakellmcontainer"
)

// container is the fakellm container shared by the integration tests of this
// package; tests isolate themselves by model name.
var container *fakellmcontainer.Container

func TestMain(m *testing.M) {
	os.Exit(fakellmcontainer.RunTests(m, func(c *fakellmcontainer.Container) { container = c }))
}
