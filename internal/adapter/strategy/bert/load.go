// SPDX-License-Identifier: Apache-2.0

package bert

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

// Defaults of the runtime options.
const (
	DefaultIntraOpThreads = 4
	DefaultQueueSize      = 64
)

// Config locates the artifacts and sizes the scoring pool.
type Config struct {
	// TokenizerPath is the XLM-R tokenizer.json.
	TokenizerPath string
	// ModelPath is the FP32 graph.onnx (sitting next to model.safetensors) or
	// the self-contained INT8 model.int8.onnx.
	ModelPath string
	// Variant must match the file: the INT8 file carries triage.variant=int8.
	Variant Variant
	// MaxInputTokens is bert.max_input_tokens; 0 means 512.
	MaxInputTokens int
	// IntraOpThreads is bert.intra_op_threads; 0 means 4.
	IntraOpThreads int
	// MaxConcurrency is bert.max_concurrency; at least 1.
	MaxConcurrency int
	// QueueSize is bert.queue_size; 0 rejects whenever every slot is busy.
	QueueSize int
}

// Load verifies the artifacts, loads the tokenizer, opens one ORT session
// with named inputs and outputs and wraps it in a bounded pool. For FP32 the
// weights file named by the graph metadata must have the recorded size and
// SHA-256 (every external-data offset is valid only for those bytes). The
// returned Strategy owns the pool and session: Close it before rt.
func Load(ctx context.Context, rt *onnxrt.Runtime, cfg Config) (*Strategy, error) {
	if rt == nil {
		return nil, fmt.Errorf("%w: nil runtime", ErrInvalidConfig)
	}
	if cfg.ModelPath == "" || cfg.TokenizerPath == "" {
		return nil, fmt.Errorf("%w: model and tokenizer paths are required", ErrInvalidConfig)
	}
	if cfg.MaxInputTokens == 0 {
		cfg.MaxInputTokens = MaxInputTokens
	}
	if cfg.IntraOpThreads == 0 {
		cfg.IntraOpThreads = DefaultIntraOpThreads
	}
	if err := checkModel(ctx, cfg.ModelPath, cfg.Variant); err != nil {
		return nil, err
	}
	tok, err := hftok.Load(cfg.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("bert: %w", err)
	}
	sess, err := rt.NewSessionFromFile(cfg.ModelPath, onnxrt.SessionConfig{
		IntraOpThreads: cfg.IntraOpThreads,
		InputNames:     []string{InputIDs, AttentionMask},
		OutputNames:    []string{OutputLogits},
	})
	if err != nil {
		return nil, fmt.Errorf("bert: %w", err)
	}
	pool, err := onnxrt.NewPool(sess, onnxrt.PoolConfig{MaxConcurrency: cfg.MaxConcurrency, QueueSize: cfg.QueueSize})
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("bert: %w", err)
	}
	s, err := New(tok, pool, cfg.Variant, WithMaxInputTokens(cfg.MaxInputTokens))
	if err != nil {
		_ = pool.Close(ctx)
		_ = sess.Close()
		return nil, err
	}
	s.closer = func(ctx context.Context) error {
		if err := pool.Close(ctx); err != nil {
			return fmt.Errorf("bert: %w", err)
		}
		if err := sess.Close(); err != nil {
			return fmt.Errorf("bert: %w", err)
		}
		return nil
	}
	return s, nil
}

// checkModel reads the model metadata and checks it against the variant;
// for FP32 it also verifies the external weights.
func checkModel(ctx context.Context, path string, v Variant) error {
	meta, err := ReadMetadataFile(path)
	if err != nil {
		return err
	}
	got := Variant(meta[MetaVariant])
	switch v {
	case VariantINT8:
		if got != VariantINT8 {
			return fmt.Errorf("%w: %s is not an INT8 model (%s=%q)", ErrInvalidModel, path, MetaVariant, got)
		}
		return nil
	case VariantFP32:
		if got != "" && got != VariantFP32 {
			return fmt.Errorf("%w: %s is not an FP32 graph (%s=%q)", ErrInvalidModel, path, MetaVariant, got)
		}
		ref, err := weightsRefOf(meta)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return ref.verify(ctx, filepath.Dir(path))
	}
	return errors.Join(ErrInvalidConfig, fmt.Errorf("bert: variant %q (want %q or %q)", v, VariantFP32, VariantINT8))
}
