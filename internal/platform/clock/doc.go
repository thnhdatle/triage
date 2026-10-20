// SPDX-License-Identifier: Apache-2.0

// Package clock provides the injectable Clock, the real clock
// and a Fake clock for tests.
//
//	c := clock.Real()
//	select {
//	case <-c.After(time.Second):
//	case <-ctx.Done():
//	}
package clock
