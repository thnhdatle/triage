// SPDX-License-Identifier: Apache-2.0

package cpu

import "runtime"

// PhysicalCores returns the number of physical CPU cores, at least 1.
func PhysicalCores() int {
	if n := physicalCores(); n > 0 {
		return n
	}
	return max(1, runtime.NumCPU())
}
