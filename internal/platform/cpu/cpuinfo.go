// SPDX-License-Identifier: Apache-2.0

package cpu

import "strings"

// countCores counts the unique (physical id, core id) pairs of a
// /proc/cpuinfo text. It returns 0 when the text has no core ids (some ARM
// kernels), so the caller falls back to the logical CPU count.
func countCores(cpuinfo string) int {
	type core struct{ phys, id string }
	seen := map[core]bool{}
	var phys, id string
	var haveID bool
	flush := func() {
		if haveID {
			seen[core{phys, id}] = true
		}
		phys, id, haveID = "", "", false
	}
	for line := range strings.SplitSeq(cpuinfo, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "physical id":
			phys = strings.TrimSpace(v)
		case "core id":
			id, haveID = strings.TrimSpace(v), true
		}
	}
	flush()
	return len(seen)
}
