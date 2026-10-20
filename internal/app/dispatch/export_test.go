// SPDX-License-Identifier: Apache-2.0

package dispatch

import "time"

// BackoffForTest exposes the jittered backoff of retry n to the tests.
func (d *Dispatcher) BackoffForTest(n int) time.Duration { return d.backoff(n) }
