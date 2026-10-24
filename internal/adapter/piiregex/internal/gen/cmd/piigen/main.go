// SPDX-License-Identifier: Apache-2.0

// Command piigen regenerates the labeled PII data set.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/thnhdatle/triage/internal/adapter/piiregex/internal/gen"
)

func main() {
	out := flag.String("out", "testdata/pii", "output directory")
	seed := flag.Int64("seed", gen.Seed, "random seed")
	flag.Parse()
	if err := gen.Write(*out, *seed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
