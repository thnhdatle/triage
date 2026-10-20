// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package bert_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

// envRealDir names an entry directory produced by `triage models pull bert
// [--int8]` (graph.onnx, model.safetensors, model.int8.onnx).
const envRealDir = "TRIAGE_BERT_REAL_DIR"

// BenchmarkScoreReal measures Score latency on the real checkpoint at 128,
// 256 and 512 tokens with 4 intra-op threads, for each variant present in
// $TRIAGE_BERT_REAL_DIR. It also proves that a freshly pulled FP32 graph
// loads. Without the variable it does nothing (a benchmark, not a test).
func BenchmarkScoreReal(b *testing.B) {
	dir := os.Getenv(envRealDir)
	if dir == "" {
		b.Skipf("set %s to a pulled bert entry directory", envRealDir)
	}
	rt, err := onnxrt.Open()
	require.NoError(b, err)
	defer func() { require.NoError(b, rt.Close()) }()
	tok := filepath.Join(dir, "tokenizer.json")
	text := strings.Repeat("Explain the difference between a mutex and a semaphore, with an example in Go. ", 80)
	for _, v := range []struct {
		name    string
		file    string
		variant bert.Variant
	}{
		{name: "fp32", file: bert.FP32FileName, variant: bert.VariantFP32},
		{name: "int8", file: bert.INT8FileName, variant: bert.VariantINT8},
	} {
		if _, err := os.Stat(filepath.Join(dir, v.file)); err != nil {
			continue
		}
		for _, tokens := range []int{128, 256, 512} {
			b.Run(fmt.Sprintf("%s/%d", v.name, tokens), func(b *testing.B) {
				s, err := bert.Load(b.Context(), rt, bert.Config{
					TokenizerPath: tok, ModelPath: filepath.Join(dir, v.file), Variant: v.variant,
					MaxInputTokens: tokens, IntraOpThreads: 4, MaxConcurrency: 1, QueueSize: 1,
				})
				require.NoError(b, err)
				defer func() { require.NoError(b, s.Close(context.Background())) }()
				req := userReq(text)
				for b.Loop() {
					if _, err := s.Score(b.Context(), req); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
