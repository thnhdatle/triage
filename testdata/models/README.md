# Model fixtures

Tiny model fixtures for the `bert` and `mf` strategy tests. All are seeded and randomly
initialised; none is derived from a real checkpoint, and real weights are never committed.

| Directory | Content |
|---|---|
| `bert-tiny/` | XLM-R classifier, 1 layer, hidden 8, full 250,002 vocabulary: `config.json`, `model.safetensors`, weight-free `graph.onnx` (same layout as `models/bert/graph.onnx`), INT8 `model.int8.onnx`, `reference.jsonl` |
| `mf-tiny/` | `model.safetensors` with the real `mf` tensor names and shapes, random values; `reference.json` (vector `u`, 200 reference scores) |
| `embeddings/` | 200 synthetic unit vectors of dimension 1536 in `embeddings.safetensors`, `meta.json` |

The XLM-R `tokenizer.json` (17 MB) is not committed. Tests use the copy cached by
`make bootstrap` (SHA-256 `fixtures.xlmr_tokenizer_sha256` in `models/manifest.yaml`).
