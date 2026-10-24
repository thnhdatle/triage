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

const (
	DefaultIntraOpThreads = 4
	DefaultQueueSize      = 64
)

type Config struct {
	TokenizerPath  string
	ModelPath      string
	Variant        Variant
	MaxInputTokens int
	IntraOpThreads int
	MaxConcurrency int
	QueueSize      int
}

// Load builds a Strategy from the model and tokenizer files in cfg.
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
