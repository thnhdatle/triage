# Model fixtures

Regenerate the tiny model fixtures used by the `bert` and `mf` strategy tests and by the parity
acceptance test on CI ([acceptance/README.md](../../acceptance/README.md)).

All are seeded and random-initialised. None is derived from a real checkpoint. Real weights are
never committed.

| Directory | Content |
|---|---|
| `bert-tiny/` | XLM-R classifier, 1 layer, hidden 8, full 250,002 vocabulary: `config.json`, `model.safetensors`, weight-free `graph.onnx` (external data into `model.safetensors`, layout of `models/bert/graph.onnx`), INT8 `model.int8.onnx`, `reference.jsonl` |
| `mf-tiny/` | `model.safetensors` with the real `mf` tensor names and shapes, random values; `reference.json` (vector `u`, 200 reference scores) |
| `embeddings/` | 200 vectors of dimension 1536 in `embeddings.safetensors`, `meta.json` |

**The embeddings are synthetic**: seeded Gaussian vectors of unit length, not recorded
`text-embedding-3-small` vectors (`meta.json`: `"synthetic": true`). No OpenAI API key was
available. They exercise the `mf` arithmetic only.

The XLM-R `tokenizer.json` (17 MB) is not committed. Tests use the copy cached by
`make bootstrap` (SHA-256 `fixtures.xlmr_tokenizer_sha256` in `models/manifest.yaml`).

## Regenerate

```sh
cd research && uv run python -m triage_research.make_fixtures   # byte-for-byte reproducible
```

`research/tests/test_fixtures.py` checks the output. Formats, tensor names, shapes, reference
computation and sample scores: `research/README.md`, section "Go-facing fixture contract".
