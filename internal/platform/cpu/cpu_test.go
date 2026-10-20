// SPDX-License-Identifier: Apache-2.0

package cpu

import (
	"runtime"
	"testing"
)

func TestPhysicalCores(t *testing.T) {
	t.Parallel()
	n := PhysicalCores()
	if n < 1 || n > max(1, runtime.NumCPU()) {
		t.Fatalf("PhysicalCores() = %d with %d logical CPUs", n, runtime.NumCPU())
	}
}

func TestCountCores(t *testing.T) {
	t.Parallel()
	const smt = `processor : 0
physical id : 0
core id : 0

processor : 1
physical id : 0
core id : 1

processor : 2
physical id : 0
core id : 0

processor : 3
physical id : 1
core id : 0

processor : 4
physical id : 0
core id : 1
`
	for name, c := range map[string]struct {
		in   string
		want int
	}{
		"smt two sockets": {smt, 3},
		"no core ids":     {"processor : 0\n\nprocessor : 1\n", 0},
		"empty":           {"", 0},
		"no trailing nl":  {"physical id : 0\ncore id : 0", 1},
	} {
		if got := countCores(c.in); got != c.want {
			t.Errorf("%s: got %d want %d", name, got, c.want)
		}
	}
}
