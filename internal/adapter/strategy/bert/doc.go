// SPDX-License-Identifier: Apache-2.0

// Package bert implements the RouteLLM BERT difficulty strategy on ONNX
// Runtime and the derivers of its ONNX artifacts.
//
// [Strategy] tokenises the scored text with the XLM-R tokenizer
// (platform/hftok), right-truncates it to bert.max_input_tokens (at most 512,
// keeping the final </s>), runs the graph through a bounded onnxrt pool and
// returns p_strong = softmax(logits)[0]. A full scoring queue fails at once
// with an error wrapping onnxrt.ErrQueueFull, whose Rejected() method makes
// the route stage record cause "rejected" and apply difficulty.on_error.
//
// [FP32Deriver] installs the committed weight-free graph next to the
// downloaded model.safetensors after checking the weights' size and SHA-256
// against the graph metadata. [INT8Deriver] runs the research converter
// (export_bert, block-wise MatMulNBits by default) through uv or the
// converter image via an injectable [CommandRunner].
//
// Usage:
//
//	s, err := bert.Load(ctx, rt, bert.Config{
//		TokenizerPath: tokPath, ModelPath: int8Path, Variant: bert.VariantINT8,
//		MaxConcurrency: 2, QueueSize: 64,
//	})
//	if err != nil {
//		return err
//	}
//	defer s.Close(ctx)
//	score, err := s.Score(ctx, req)
package bert
