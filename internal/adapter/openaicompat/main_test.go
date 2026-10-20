// SPDX-License-Identifier: Apache-2.0

//go:build !integration

package openaicompat_test

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
