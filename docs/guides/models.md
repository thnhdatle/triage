# Models

Download and check the model files for `bert`, `mf` and NER.

Nothing is bundled or downloaded at serve time. Pull the files once with `triage models`.

| Entry | Source (pinned revision in `models/manifest.yaml`) | Used by | Size on disk |
|---|---|---|---|
| `bert` | `routellm/bert_gpt4_augmented` | strategy `bert`; `selection.tokenizer: bert` | about 1.1 GB, plus the ONNX model |
| `mf` | `routellm/mf_gpt4_augmented` | strategy `mf` | under 1 MB |
| `ner` | `dslim/bert-base-NER` | `privacy.ner.enabled` | about 430 MB |

## Commands

```sh
triage models pull bert --int8            # one entry, plus the INT8 build
triage models pull all --int8             # every entry (same as make models)
triage models pull --config triage.yaml   # only what this configuration needs
triage models list                        # what is on disk, re-verified
```

| Flag | Meaning |
|---|---|
| `--config` | configuration file (default `$TRIAGE_CONFIG`) |
| `--cache-dir` | overrides `models.cache_dir` |
| `--research-dir` | `research/` checkout for INT8 (default `$TRIAGE_RESEARCH_DIR`, then `./research`) |
| `--int8` | also build the INT8 `bert` model |

`pull` is safe to repeat. Without names and without a configuration it fails.

`triage models list` prints one row per entry:

```text
NAME  REVISION      FILES          DERIVED                          ACTIVE     EXPECTED LATENCY (p50)                       SUGGESTED TIMEOUT
bert  86237e3df400  4/4 verified   onnx-fp32:built onnx-int8:built  onnx-int8  128 tok 31 ms, 256 tok 61 ms, 512 tok 134 ms  300ms
```

`ACTIVE` is the variant `variant: auto` uses. Latency is measured on a MacBook Pro M3 Pro.
**FP32 can exceed the default 300 ms timeout**: set `difficulty.bert.timeout: 500ms` for FP32.

## Manifest and cache

Files go to `models.cache_dir` (default `~/.cache/rayld/triage/models`). Every file is checked
by SHA-256. Do not commit or copy `manifest.local.yaml` to another machine; run `pull` on each
machine instead.

`serve` fails at startup or reload if a needed file is missing. Use `triage models list` to
re-verify files.

## Derived artifacts

| Derived file | Built by | Needs |
|---|---|---|
| `bert` FP32 ONNX | always | nothing else (no Python) |
| `bert` INT8 ONNX (`model.int8.onnx`) | `--int8` | `uv` with a `research/` checkout, or a container engine |
| `mf` vector | always | nothing else |
| `ner` | not derived | nothing else |

### FP32 through the weight-free graph

`pull` installs the FP32 model without Python. No weights are converted or republished.

### INT8

`pull bert --int8` uses `uv` with a `research/` checkout if found. Otherwise it runs the
converter image `$RAYLD_TRIAGE_MODELS_IMAGE` (default `ghcr.io/thnhdatle/triage-models`), then
the local `thnhdatle/triage-models:dev`.

```sh
make docker                      # builds thnhdatle/triage-models:dev
triage models pull bert --int8   # no research/ here: uses the image, :dev as fallback
```

It takes about a minute. The result is `model.int8.onnx`, 291 MB.

`difficulty.bert.variant`: `auto` uses INT8 when built, else FP32. `int8` and `fp32` fail
validation if that variant is missing.

## Licenses and provenance

- `bert`: Apache-2.0 `LICENSE` file; no model card.
- `mf`: no license file. Triage only downloads it; it does not redistribute it.
- `ner`: no license file; the model card states MIT. English news text (CoNLL-2003).

Both RouteLLM checkpoints were trained with GPT-4 as strong and Mixtral-8x7B as weak.
Triage publishes nothing derived from these weights.

## Builds without ONNX Runtime

`go build -tags noonnx` (or `CGO_ENABLED=0`) has no ONNX Runtime.

- Works: `random`, `length`, `mf`, rules, `selection.tokenizer: bert`.
- `strategy: bert` fails validation, unless `difficulty.missing_artifact: fallback`.
- `privacy.ner.enabled: true` always fails validation.

Other builds need the [ONNX Runtime library](operations.md#onnx-runtime-library).
