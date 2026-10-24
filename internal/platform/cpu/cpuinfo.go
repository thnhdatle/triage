// SPDX-License-Identifier: Apache-2.0

package cpu

import "strings"

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
