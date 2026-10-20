# triage-research

Reproduce the Triage model artifacts and their reference values. No training code, no training
data, no committed weights. Nothing here is imported by Go.

`routellm_ref/` vendors the minimal RouteLLM inference code (lm-sys/RouteLLM @ `0b64fdaf`,
Apache-2.0, changes marked `triage:`).

## Setup and checks

Python 3.12, managed with [uv](https://docs.astral.sh/uv/).

```sh
cd research && uv sync    # torch from the CPU index on Linux
uv run ruff format --check . && uv run ruff check . && uv run mypy --strict src tests && uv run pytest -q
```

The tests need the XLM-R `tokenizer.json` cached by `make bootstrap`.

## Reproducing each artifact

Pins, SHA-256 and sizes: `../models/manifest.yaml`. Checkpoints go to
`~/.cache/rayld/triage/models/<name>/<revision>/` (the default `--checkpoint-dir`).

| Artifact | Command |
|---|---|
| Checkpoints (bert, mf, ner) | `uv run python -m triage_research.fetch bert\|mf\|ner\|all` (SHA-256 verified, never `optimizer.pt`) |
| `models/bert/graph.onnx` | `uv run python -m triage_research.build_bert_graph` (`--check`: fail if the committed file differs from a fresh build) |
| bert INT8 ONNX | `uv run python -m triage_research.export_bert [--checkpoint-dir D] [--out F] [--scheme blockwise\|dynamic]` |
| `testdata/models/` | `uv run python -m triage_research.make_fixtures` (seeded, byte-for-byte reproducible) |
| `testdata/models/embeddings/` | `uv run python -m triage_research.record_embeddings [--record]` |
| bert reference scores | `uv run python -m triage_research.reference_bert --backend torch\|onnx [--model F] [--out F]` |
| mf reference scores | `uv run python -m triage_research.reference_mf [--checkpoint-dir D] [--out F]` |
| Offline APGR (cross-check of `triage eval`, agreement within 1e-9) | `uv run python -m triage_research.eval_offline SCORES.{csv,jsonl} [--strategy S] [--curve]` |
| Reference token ids (`testdata/tokenizer/`) | `uv run python -m triage_research.reference_tokenize corpus\|reference\|tables` |
| Token estimate bound, plots | `token_estimate_bound`, `plot_eval`: stubs (`NotImplementedError`) |

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

## Go-facing fixture contract (`testdata/models/`)

All fixtures are seeded random values, not derived from any checkpoint. Paths are relative to
`testdata/models/`. The XLM-R tokenizer is **not** committed: use
`~/.cache/rayld/triage/tokenizers/bert/tokenizer.json` (SHA-256
`fixtures.xlmr_tokenizer_sha256` in `models/manifest.yaml`, fetched by `make bootstrap`).

### `bert-tiny/` (1 layer, hidden 8, 2 heads, FFN 16, full 250,002 vocabulary, 514 positions)

| File | Content |
|---|---|
| `config.json` | `XLMRobertaForSequenceClassification` config, 3 labels |
| `model.safetensors` | every model parameter (same names as the real checkpoint), F32, seeded N(0, 1.5^2) (8.0 MB) |
| `graph.onnx` | weight-free FP32 graph, external data into `model.safetensors` in the same directory (load it in place) |
| `model.int8.onnx` | self-contained INT8 (`blockwise`), 2.1 MB |
| `reference.jsonl` | one line per prompt of `testdata/tokenizer/corpus.jsonl` (300, same order): `{"id", "n_tokens", "p_torch", "p_fp32", "p_int8", "logits_fp32"}` |

Inputs per line:

- ids: tokenizer `encode(text)` with `<s>`/`</s>`, right-truncated to 512 keeping the final
  `</s>` (= `hftok.Truncate(ids, 512)`); batch 1; `attention_mask` all ones.
- `n_tokens`: length after truncation (15 prompts hit 512).
- `p_fp32` / `p_int8`: Python ONNX Runtime 1.31.0 (1 thread) on `graph.onnx` /
  `model.int8.onnx`, `p_strong = softmax(logits)[0]` in float64.
- `p_torch`: RouteLLM `BERTRouter` (torch).

Python ORT FP32 vs torch: max |dp| 1.6e-6. `p_int8` differs from `p_fp32` by up to 0.16
(random tiny weights). On fixtures the INT8 routing agreement is reported, not checked against
a threshold.

Sample values (`corpus.jsonl` text, `n_tokens`, `p_fp32`, `p_int8`):

| id | text | n_tokens | p_fp32 | p_int8 |
|---|---|---|---|---|
| `en-001` | `What is the capital of Australia, and why isn't it Sydney?` | 17 | 0.023702140779158314 | 0.017279956632338278 |
| `en-002` | `Barack Obama was born in Honolulu, Hawaii, on August 4, 1961.` | 19 | 0.003870394013564082 | 0.0037360167345194257 |
| `code-001` | (see corpus) | 36 | 0.11676017960924248 | 0.1122911529627962 |
| `long-001` | (see corpus) | 512 | 0.0008718896660294876 | 0.0008349411301045883 |

`en-001` ids: `[0, 4865, 83, 70, 10323, 111, 28173, 4, 136, 15400, 56112, 25, 18, 442, 87547, 32, 2]`,
FP32 logits `[-1.0415518283843994, -0.7435486316680908, 2.643397808074951]`.

Suggested Go assertions (the bounds of the parity acceptance test):

- Go ORT FP32 vs `p_fp32`, Go ORT INT8 vs `p_int8`: within 1e-4.
- Go FP32 vs `p_torch`: within 1e-4.

`p_int8` was produced on darwin/arm64 (the INT8 kernels quantise activations at run time). On
other CPUs, if the 1e-4 bound is missed, compare INT8 against a live Python run
(`reference_bert --backend onnx`) instead of the committed value.

### `mf-tiny/`

| File | Content |
|---|---|
| `model.safetensors` | real names and shapes: `P.weight` F32 [64, 128], `classifier.0.weight` F32 [1, 128], `text_proj.0.weight` F32 [128, 1536]; seeded N(0, 1) |
| `reference.json` | `{"strong_row": 24, "weak_row": 36, "u": [1536 float64], "scores": [{"id", "p_mfmodel", "p_strong"}]}`, 200 scores in embedding row order |

- `u = text_proj.0.weight^T (classifier.0.weight[0] * (P^[24] - P^[36]))`, float64.
- `P^` = rows of `P.weight` divided by max(L2 norm, 1e-12).
- `p_strong = sigmoid(u . e)` (float64).
- Rows: 24 = `gpt-4-1106-preview`, 36 = `mixtral-8x7b-instruct-v0.1` (RouteLLM `MODEL_IDS`).
- `p_mfmodel`: RouteLLM `MFModel` (torch float32); max |p_mfmodel - p_strong| = 1.4e-7 (real
  checkpoint: 5.9e-8).

Samples: `u[0..2] = 1.3384389001778991, -0.8661962262449422, -1.3648144952697887`; row 0
`p_strong = 0.04355773491008071`, row 1 `0.5944606850097978`, row 2 `0.23604344974153227`.

### `embeddings/`

| File | Content |
|---|---|
| `embeddings.safetensors` | tensor `embeddings`, F32 [200, 1536] |
| `meta.json` | `{"model": "text-embedding-3-small", "dimensions": 1536, "count": 200, "synthetic": true, "seed": 20261019, "ids": ["synthetic-000", ...]}` |

**The committed vectors are synthetic** (`synthetic: true`): seeded Gaussian vectors with unit
L2 norm, from `record_embeddings` without `--record`. With an OpenAI key,
`record_embeddings --record` embeds the first 200 corpus prompts and sets `synthetic: false`
with corpus ids.

### Live references on real artifacts

The parity acceptance test compares Go against these on the real checkpoints.

```sh
uv run --project research python -m triage_research.reference_bert --backend torch --out torch.jsonl   # PyTorch
uv run --project research python -m triage_research.reference_bert --backend onnx --out fp32.jsonl     # ORT FP32
uv run --project research python -m triage_research.reference_bert --backend onnx \
  --model ~/.cache/rayld/triage/models/bert/<rev>/model.int8.onnx --out int8.jsonl                     # ORT INT8
uv run --project research python -m triage_research.reference_mf --out mf.json                         # mf
```

Each `reference_bert` line is `{"id", "n_tokens", "logits", "p_strong"}`. `--prompts` takes any
JSONL with `id` and `text` (default: the tokenizer corpus).
