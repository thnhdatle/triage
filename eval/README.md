# eval/

Inputs of `triage eval`. Usage: [docs/guides/evaluation.md](../docs/guides/evaluation.md).

| Path | Content |
|---|---|
| `live.yaml` | live run, LM Studio pair, 15 calibration + 15 evaluation items |
| `labels-mmlu.yaml`, `labels-gsm8k.yaml` | offline APGR on published labels, 300 MMLU and 100 GSM8K items |
| `sets/` | committed item-id lists |
| `prompts/` | live-mode prompt templates |

`make eval` runs the three configurations in about 10 minutes on a MacBook Pro M3 Pro. The sets are
small on purpose: results show that routing works, not precise accuracy.

## Data

```sh
scripts/fetch_eval_data.sh   # pinned revisions, SHA-256 verified, into ~/.cache/rayld/triage/eval/data
```

| Source | Files |
|---|---|
| GSM8K test (`openai/gsm8k`) | `gsm8k/test.jsonl` |
| 57 MMLU subject test splits (`cais/mmlu`) | `mmlu/test/<subject>.jsonl` |
| Label files and contaminated prompts (`lm-sys/RouteLLM` evals, Apache-2.0) | `routellm/` |

Set `EVAL_DATA_DIR` to change it (eval config: `dataset.data_dir`).

## Item ids

One id per line in a set file. Blank lines and lines starting with `#` are ignored.

| Id | Item |
|---|---|
| `gsm8k/test:<row>` | zero-based row of the GSM8K test split (line `row` of `gsm8k/test.jsonl`) |
| `gsm8k/test:*` | all GSM8K test rows except contaminated prompts |
| `mmlu/test:<subject>:<row>` | zero-based row of one MMLU subject's test split (`mmlu/test/<subject>.jsonl`) |

Template fields (Go `text/template`): `.Question`; MMLU also `.Subject`, `.Choices` with
`.Letter` and `.Text`. Labels mode takes prompt and correctness from the label file.

## Sets

Drawn with seed 7 from the fetched data by `uv run python -m triage_research.make_eval_sets sample
--data <data dir>` (run from `research/`). Contaminated prompts are excluded.

| File | Items |
|---|---|
| `sets/live_cal.txt` | 8 GSM8K + 7 MMLU, calibration |
| `sets/live_eval.txt` | 8 GSM8K + 7 MMLU, evaluation (disjoint) |
| `sets/labels_mmlu.txt` | 300 labelled MMLU items |
| `sets/labels_gsm8k.txt` | 100 GSM8K items |

Each run writes `out/eval/<UTC timestamp>/`, with the cost-quality curve in `curve.csv`.
