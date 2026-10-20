// SPDX-License-Identifier: Apache-2.0

package cpu

import "runtime"

// PhysicalCores returns the number of physical cores (not hardware threads),
// at least 1. It falls back to runtime.NumCPU when the platform does not
// report it.
func PhysicalCores() int {
	if n := physicalCores(); n > 0 {
		return n
	}
	return max(1, runtime.NumCPU())
}
