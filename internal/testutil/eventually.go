// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"testing"
	"time"
)

// Eventually polls cond until it returns true or the timeout elapses, then fails the test.
func Eventually(tb testing.TB, cond func() bool, timeout time.Duration) {
	tb.Helper()
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("condition not met within %v", timeout)
		}
		<-tick.C
	}
}
