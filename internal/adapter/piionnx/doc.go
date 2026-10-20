// SPDX-License-Identifier: Apache-2.0

// Package piionnx implements the optional English NER PII detector (
// privacy.ner) on ONNX Runtime with dslim/bert-base-NER, a cased BERT-base
// fine-tuned on CoNLL-2003. It is English only.
//
// [Detector] implements privacy.Detector:
//
//   - Tokenization uses the pinned onnx/tokenizer.json through
//     internal/platform/hftok (ids identical to HF tokenizers). The
//     ids are aligned back to byte offsets of the original text: the
//     tokenizer neither lowercases nor strips accents, so every token except
//     [UNK] occurs verbatim once removed characters (controls, format
//     characters, invalid UTF-8) are skipped. Findings therefore carry exact
//     byte spans on rune boundaries.
//   - Texts longer than one window are scanned in windows of up to 512
//     tokens ([CLS] and [SEP] included) overlapping by 128 tokens, batched in
//     one run; each token's prediction comes from the window where it is
//     farthest from an edge.
//   - Labels come from onnx/config.json id2label. Words (a token and its
//     "##" continuations) take the argmax of their tokens' mean
//     probabilities; BIO decoding groups words into entities. PER, LOC and
//     ORG become findings of type person, location and organization; MISC is
//     ignored. An entity is kept when its mean word score is at least
//     min_score (default 0.8).
//   - A text above max_tokens (default 2048), a timeout (default 200 ms,
//     queueing included), a full queue or an inference error yields one
//     ner_unavailable finding without value (fail-closed), never an error;
//     only the end of the caller's context is returned as an error.
//   - Inference runs on one shared ONNX session behind an onnxrt.Pool that
//     bounds concurrent runs and waiting callers.
//
// Text is never logged; a ner_unavailable event logs its reason and the
// text's byte length.
//
// Tests use a tiny hand-encoded ONNX token-classification model and the
// tokenizer cached by `make bootstrap` (~/.cache/rayld/triage/tokenizers/ner,
// or TRIAGE_TOKENIZER_DIR). A test against the real model runs only with
// `go test -tags realmodel ./internal/adapter/piionnx/` after `triage models
// pull ner` (or with TRIAGE_NER_MODEL_DIR pointing at the onnx/ directory).
//
// Usage:
//
//	det, err := piionnx.New(rt, piionnx.Files{
//		Model:     dir + "/onnx/model.onnx",
//		Tokenizer: dir + "/onnx/tokenizer.json",
//		Config:    dir + "/onnx/config.json",
//	}, piionnx.WithMinScore(cfg.MinScore), piionnx.WithMaxTokens(cfg.MaxTokens),
//		piionnx.WithTimeout(cfg.Timeout), piionnx.WithLogger(log))
//	if err != nil {
//		return err
//	}
//	defer det.Close(ctx)
//	if err := det.Probe(ctx); err != nil {
//		return err
//	}
//	findings, err := det.Detect(ctx, text)
package piionnx
