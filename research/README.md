# triage-research

Python tools for the Triage model artifacts: the checkpoint fetcher, the ONNX converters used by
`triage models pull bert --int8`, and the converter for the eval data. No training code, no
training data, no committed weights. Go does not import anything from here.

## Setup and checks

Python 3.12, managed with [uv](https://docs.astral.sh/uv/).

```sh
cd research && uv sync    # torch from the CPU index on Linux
uv run ruff format --check . && uv run ruff check . && uv run mypy --strict src tests && uv run pytest -q
```

## Commands

Pins, SHA-256 and sizes: `../models/manifest.yaml`. Checkpoints go to
`~/.cache/rayld/triage/models/<name>/<revision>/` (the default `--checkpoint-dir`).

| Artifact | Command |
|---|---|
| Checkpoints (bert, mf, ner) | `uv run python -m triage_research.fetch bert\|mf\|ner\|all` (SHA-256 verified, never `optimizer.pt`) |
| `models/bert/graph.onnx` | `uv run python -m triage_research.build_bert_graph` (`--check`: fail if the committed file differs from a fresh build) |
| bert INT8 ONNX | `uv run python -m triage_research.export_bert [--checkpoint-dir D] [--out F] [--scheme blockwise\|dynamic]` |
| Eval data and sets | `uv run python -m triage_research.make_eval_sets convert\|sample` (used by `scripts/fetch_eval_data.sh`) |

## Graph contract (`models/bert/graph.onnx` and `bert-tiny/graph.onnx`)

- TorchScript exporter, opset 17, `ir_version` 8, no constant folding.
- Inputs `input_ids`, `attention_mask`: int64 `[batch, sequence]`. Output `logits`: float32
  `[batch, 3]`. `p_strong = softmax(logits)[0]`.
- No embedded weights (the real graph is 253 KB). Every initializer is ONNX external data with
  `location = model.safetensors` and absolute byte `offset`/`length` into the unmodified
  safetensors file.
- The graph must sit in the same directory as `model.safetensors` (copy it there; no symlink
  needed). ONNX Runtime resolves the location relative to the graph file.
- `metadata_props`: `triage.weights.file` (`model.safetensors`), `triage.weights.sha256`,
  `triage.weights.size`, `triage.source`, `triage.converter`, `triage.p_strong`. INT8 adds
  `triage.variant=int8`, `triage.int8.scheme`, `triage.int8.converter`.
- **Verify `triage.weights.sha256` and `triage.weights.size` against the file before loading.**
  They make every offset valid.
- INT8 (`export_bert`, default `blockwise`): one self-contained file, same inputs and outputs,
  opsets `ai.onnx` 17 and `com.microsoft` 1. Weight MatMuls become `com.microsoft.MatMulNBits`
  (8-bit, block 32, accuracy level 4). The three embedding tables are int8 initializers read by
  `Gather` + `DequantizeLinear`.
- `--scheme dynamic` (plain `quantize_dynamic`) misses the required routing agreement with
  PyTorch on the real checkpoint (0.97 at strong-call ratios 0.3 and 0.5): 91.7% / 88.7% vs
  99.7% / 98.3%.

Converter CLI used by `triage models pull bert --int8`:

```sh
export_bert --checkpoint-dir <dir with config.json + model.safetensors> --out <file>   # last stdout line: <sha256>  <path>
```

- Exits 1 with a message on stderr if the graph's `triage.weights.sha256` does not match.
- Without `--graph`: uses the committed graph in a checkout, otherwise rebuilds the identical
  graph in memory from `config.json` (the `triage-models` image).
