// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package cpu

import "golang.org/x/sys/unix"

func physicalCores() int {
	n, err := unix.SysctlUint32("hw.physicalcpu")
	if err != nil {
		return 0
	}
	return int(n)
}
