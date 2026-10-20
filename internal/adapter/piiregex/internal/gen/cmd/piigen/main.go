// SPDX-License-Identifier: Apache-2.0

// Command piigen regenerates testdata/pii/labelled.jsonl and negatives.jsonl.
//
//	go run ./internal/adapter/piiregex/internal/gen/cmd/piigen -out testdata/pii
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
