// SPDX-License-Identifier: Apache-2.0

package dispatch

import "time"

func (d *Dispatcher) BackoffForTest(n int) time.Duration { return d.backoff(n) }
