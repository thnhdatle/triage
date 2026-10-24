// SPDX-License-Identifier: Apache-2.0

// Package gen generates the labeled PII data set under testdata/pii from a seed.
package gen

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

const (
	Seed              int64 = 20261019
	PositivesPerType        = 140
	NegativeCount           = 1000
	LabelledFile            = "labelled.jsonl"
	NegativesFile           = "negatives.jsonl"
	dirMode                 = 0o750
	fileMode                = 0o600
	positiveTypeCount       = 10
)

type Span struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
}

type Sample struct {
	Text  string `json:"text"`
	Spans []Span `json:"spans"`
	Lang  string `json:"lang"`
}

type builder struct {
	sb    strings.Builder
	spans []Span
}

func (b *builder) lit(s string) { b.sb.WriteString(s) }

func (b *builder) pii(typ, v string) {
	start := b.sb.Len()
	b.sb.WriteString(v)
	b.spans = append(b.spans, Span{Start: start, End: b.sb.Len(), Type: typ})
}

func (b *builder) sample(lang string) Sample {
	spans := b.spans
	if spans == nil {
		spans = []Span{}
	}
	return Sample{Text: b.sb.String(), Spans: spans, Lang: lang}
}

type generator struct{ r *rand.Rand }

func newGenerator(seed int64) *generator {
	return &generator{r: rand.New(rand.NewSource(seed))} //nolint:gosec // G404: deterministic test data
}

func (g *generator) intn(n int) int { return g.r.Intn(n) }

func (g *generator) between(lo, hi int) int { return lo + g.r.Intn(hi-lo+1) }

func (g *generator) pick(list string) string {
	items := strings.Split(list, "|")
	return items[g.r.Intn(len(items))]
}

func (g *generator) chance(percent int) bool { return g.r.Intn(100) < percent }

func (g *generator) digits(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = "0123456789"[g.r.Intn(10)]
	}
	return string(b)
}

func (g *generator) charset(n int, set string) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = set[g.r.Intn(len(set))]
	}
	return string(b)
}

// Positives returns the samples that contain PII.
func Positives(seed int64) []Sample {
	g := newGenerator(seed)
	types := strings.Split(AllTypes, "|")
	out := make([]Sample, 0, PositivesPerType*positiveTypeCount)
	for i := range PositivesPerType {
		for _, typ := range types {
			lang := langOf(i)
			var b builder
			g.segment(&b, typ, lang)
			if g.chance(15) {
				b.lit("\n")
				g.segment(&b, g.pick(AllTypes), lang)
			}
			out = append(out, b.sample(lang))
		}
	}
	return out
}

// Negatives returns the samples that contain no PII.
func Negatives(seed int64) []Sample {
	g := newGenerator(seed + 1)
	out := make([]Sample, 0, NegativeCount)
	for i := range NegativeCount {
		out = append(out, g.negative(langOf(i)))
	}
	return out
}

// Write writes the data set files into dir.
func Write(dir string, seed int64) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("gen: create %s: %w", dir, err)
	}
	if err := writeJSONL(filepath.Join(dir, LabelledFile), Positives(seed)); err != nil {
		return err
	}
	return writeJSONL(filepath.Join(dir, NegativesFile), Negatives(seed))
}

func writeJSONL(path string, samples []Sample) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode) //nolint:gosec // G304: caller-chosen output
	if err != nil {
		return fmt.Errorf("gen: open %s: %w", path, err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, s := range samples {
		if err := enc.Encode(s); err != nil {
			_ = f.Close()
			return fmt.Errorf("gen: encode %s: %w", path, err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("gen: flush %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("gen: close %s: %w", path, err)
	}
	return nil
}
