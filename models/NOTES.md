# Manifest notes

Facts about the pinned repositories that `manifest.yaml` relies on.

- ONNX Runtime v1.31.0 release assets for linux x64 and aarch64: pinned by SHA-256.
- `mf`: no `config.json`; only `model.safetensors` is pinned. Embedding dimension (1536) and
  model indices come from the original reference code.
- `ner`: no `tokenizer.json` at the root; `onnx/tokenizer.json` and `onnx/config.json` are
  pinned. `dslim/bert-base-NER` has no LICENSE file; its model card metadata says MIT.
- `bert` and `mf`: no README or model card at the pinned revisions. `bert`'s base model is
  `xlm-roberta-base` (from `config.json`). The authors state no training data beyond the
  paper arXiv:2406.18665.
- `bert` files the router does not need (for example `sentencepiece.bpe.model`) are not
  allow-listed and never downloaded.
