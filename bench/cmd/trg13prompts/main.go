// SPDX-License-Identifier: Apache-2.0

// Command trg13prompts regenerates bench/prompts/trg13.txt: 1,000 prompt ids
// from GSM8K test and MMLU test whose bert encoding (with <s> and </s>) has
// 128..1,024 tokens, drawn with a fixed seed.
//
//	go run ./bench/cmd/trg13prompts -tokenizer ~/.cache/rayld/triage/models/bert/<rev>/tokenizer.json
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thnhdatle/triage/bench/prompts"
	"github.com/thnhdatle/triage/internal/platform/hftok"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "trg13prompts:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defData := filepath.Join(home, ".cache", "rayld", "triage", "eval", "data")
	var (
		data    = flag.String("data", defData, "converted eval data dir")
		tokPath = flag.String("tokenizer", "", "bert tokenizer.json (required)")
		out     = flag.String("out", "bench/prompts/trg13.txt", "output id file")
		n       = flag.Int("n", 1000, "number of prompts")
		minTok  = flag.Int("min", 128, "minimum bert tokens incl. <s> and </s>")
		maxTok  = flag.Int("max", 1024, "maximum bert tokens incl. <s> and </s>")
		seed    = flag.Uint64("seed", 7, "sampling seed")
	)
	flag.Parse()
	if *tokPath == "" {
		return errors.New("-tokenizer is required")
	}
	tok, err := hftok.Load(*tokPath)
	if err != nil {
		return err
	}
	all, err := prompts.NewData(*data).All()
	if err != nil {
		return err
	}
	count := func(s string) int {
		ids, _ := tok.Encode(s, true)
		return len(ids)
	}
	sel, err := prompts.Select(all, count, *minTok, *maxTok, *n, *seed)
	if err != nil {
		return err
	}
	eligible := 0
	for _, p := range all {
		if c := count(p.Text); c >= *minTok && c <= *maxTok {
			eligible++
		}
	}
	var hist [4]int // 128..255, 256..511, 512..1023, 1024
	for _, p := range sel {
		hist[min(count(p.Text)/256, 3)]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# TRG-13 prompt set: %d ids from GSM8K test + MMLU test with %d..%d bert tokens "+
		"(incl. <s>, </s>),\n", len(sel), *minTok, *maxTok)
	fmt.Fprintf(&b, "# seed %d over the %d of %d items in range; tokens <256: %d, 256..511: %d, "+
		">=512: %d (truncated to 512).\n", *seed, eligible, len(all), hist[0], hist[1], hist[2]+hist[3])
	b.WriteString("# Regenerate: go run ./bench/cmd/trg13prompts -tokenizer <bert tokenizer.json> " +
		"(bench/prompts/README.md)\n")
	for _, p := range sel {
		b.WriteString(p.ID)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil { //nolint:gosec // G306: committed text file
		return err
	}
	_, err = fmt.Printf("wrote %s: %d ids, %d of %d items in range\n", *out, len(sel), eligible, len(all))
	return err
}
