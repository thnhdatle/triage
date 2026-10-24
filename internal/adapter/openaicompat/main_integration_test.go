// SPDX-License-Identifier: Apache-2.0

//go:build integration

package openaicompat_test

import (
	"os"
	"testing"

	"github.com/thnhdatle/triage/internal/testutil/fakellmcontainer"
)

var container *fakellmcontainer.Container

func TestMain(m *testing.M) {
	os.Exit(fakellmcontainer.RunTests(m, func(c *fakellmcontainer.Container) { container = c }))
}
