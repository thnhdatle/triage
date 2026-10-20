// SPDX-License-Identifier: Apache-2.0

//go:build linux

package cpu

import "os"

func physicalCores() int {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	return countCores(string(data))
}
