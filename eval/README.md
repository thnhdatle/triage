# eval/

Find the inputs of `triage eval` and fetch its data. Usage: [docs/guides/evaluation.md](../docs/guides/evaluation.md).
Names starting with `trg03` and `trg10` follow the criterion ids in
[../acceptance/README.md](../acceptance/README.md).

| Path | Content |
|---|---|
| `trg03.yaml` | live run, LM Studio pair; `mf` needs `OPENAI_API_KEY` |
| `trg10.yaml`, `trg10_gsm8k.yaml` | offline APGR, MMLU and GSM8K labels |
| `sets/` | committed item-id lists |
| `prompts/` | live-mode prompt templates |

## Data

```sh
scripts/fetch_eval_data.sh   # pinned revisions, SHA-256 verified, into ~/.cache/rayld/triage/eval/data
```

| Source | Files |
|---|---|
| GSM8K test (`openai/gsm8k`) | `gsm8k/test.jsonl` |
| 57 MMLU subject test splits (`cais/mmlu`) | `mmlu/test/<subject>.jsonl` |
| `routellm/evals` of `lm-sys/RouteLLM` (Apache-2.0: label files, contaminated prompts) | `routellm/` |

Set `EVAL_DATA_DIR` to change it (eval config: `dataset.data_dir`).

## Item ids

One id per line in a set file. Blank lines and lines starting with `#` are ignored.

| Id | Item |
|---|---|
| `gsm8k/test:<row>` | zero-based row of the GSM8K test split (line `row` of `gsm8k/test.jsonl`) |
| `gsm8k/test:*` | all GSM8K test rows except RouteLLM's contaminated prompts |
| `mmlu/test:<subject>:<row>` | zero-based row of one MMLU subject's test split (`mmlu/test/<subject>.jsonl`) |

Template fields (Go `text/template`): `.Question`; MMLU also `.Subject`, `.Choices` with
`.Letter` and `.Text`. Labels mode takes prompt and correctness from RouteLLM's label file.

## Sets

Generated with seed 7 from the fetched data by
`research/src/triage_research/make_eval_sets.py sample` (run from `research/`). RouteLLM's
contaminated prompts are excluded.

| File | Items |
|---|---|
| `sets/trg03_cal.txt` | 50 GSM8K + 50 MMLU, calibration |
| `sets/trg03_eval.txt` | 50 GSM8K + 50 MMLU, evaluation (disjoint) |
| `sets/trg10_mmlu.txt` | 2,000 labelled MMLU items |
| `sets/trg10_gsm8k.txt` | `gsm8k/test:*` (1,319 minus contaminated) |

## Results

```sh
triage eval --config eval/trg03.yaml   # writes out/eval/<UTC timestamp>/, cost-quality curve in curve.csv
```

TRG-10 writes `out/eval/trg10.md`.
