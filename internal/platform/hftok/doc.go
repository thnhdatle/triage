// SPDX-License-Identifier: Apache-2.0

// Package hftok loads Hugging Face tokenizer.json files and encodes text into
// token ids identical to the Rust/Python `tokenizers` library.
//
// It is shared by the bert strategy (XLM-R Unigram), the NER detector
// (WordPiece) and exact token counting for selection.tokenizer. The
// implementation is pure Go so that it also works in the CGO-free noonnx
// build. Only the tokenizer.json components the pinned models use are
// supported (normalizers Precompiled, BertNormalizer and Sequence;
// pre-tokenizers WhitespaceSplit, Metaspace, BertPreTokenizer and Sequence;
// models Unigram and WordPiece; post-processor TemplateProcessing); any other
// component makes [Load] fail with [ErrUnsupported] rather than encode
// differently from the reference.
//
// Parity with HF tokenizers is asserted on the committed corpus in
// testdata/tokenizer.
//
// Usage:
//
//	tok, err := hftok.Load(path)
//	if err != nil {
//		return err
//	}
//	ids, err := tok.Encode(prompt, true)
//	if err != nil {
//		return err
//	}
//	ids = hftok.Truncate(ids, 512)
package hftok
