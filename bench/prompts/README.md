# bench/prompts

Fetch and regenerate the benchmark prompt sets. Only item ids are committed. The texts come
from public datasets and are not redistributed here.

## Get the texts

```sh
scripts/fetch_eval_data.sh   # pinned revisions, SHA-256 verified, into ~/.cache/rayld/triage/eval/data
```

Set `EVAL_DATA_DIR` to use another directory.

| File | Used by | Content |
|---|---|---|
| `trg13.txt` | TRG-13 latency benchmark | 1,000 ids, 128–1,024 `bert` tokens incl. `<s>`/`</s>` (truncated to 512 at scoring) |

Criterion ids: [../../acceptance/README.md](../../acceptance/README.md).

## Sources and licenses

| Id prefix | Dataset | Revision | License |
|---|---|---|---|
| `gsm8k/test:` | `openai/gsm8k` (Hugging Face), config `main`, split `test` | `740312add88f781978c0658806c59bc2815b9866` | MIT |
| `mmlu/test:` | `cais/mmlu` (Hugging Face), the 57 subject `test` splits | `c30699e8356da336a370243923dbaf21066bb9fe` | MIT |

## Id grammar and texts

Ids follow `eval/README.md`. Package `github.com/thnhdatle/triage/bench/prompts` resolves them.

| Id | Text |
|---|---|
| `gsm8k/test:<row>` | `question` of zero-based row `row` of `gsm8k/test.jsonl` |
| `mmlu/test:<subject>:<row>` | `question`, then `\nA. <choice>` … per choice, then `\nAnswer:` (row `row` of `mmlu/test/<subject>.jsonl`) |

## Regenerating `trg13.txt`

```sh
scripts/fetch_eval_data.sh                                                                        # the datasets
go run ./bench/cmd/trg13prompts -tokenizer ~/.cache/rayld/triage/models/bert/<revision>/tokenizer.json   # writes trg13.txt
```

Seed 7. Same data and tokenizer give a byte-identical file.
