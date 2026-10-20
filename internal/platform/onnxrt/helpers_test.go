// SPDX-License-Identifier: Apache-2.0

package onnxrt

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain checks for leaked goroutines: Session.Run arms a context.AfterFunc
// and Pool.Acquire may block.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
