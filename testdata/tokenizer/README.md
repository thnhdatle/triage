# Tokenizer parity corpus

Regenerate the corpus that checks `internal/platform/hftok` against Hugging Face `tokenizers`.

| Tokenizer | Source | Prompts used |
|---|---|---|
| `bert` | XLM-R Unigram `tokenizer.json` of `routellm/bert_gpt4_augmented` | all 300 |
| `ner` | WordPiece `onnx/tokenizer.json` of `dslim/bert-base-NER` | the 158 with `lang == "en"` |

| File | Content |
|---|---|
| `corpus.jsonl` | 300 lines `{"id","category","text","lang"}` |
| `reference_bert.jsonl` | `{"id","ids"}` for every prompt: `<s>` (0) … `</s>` (2) |
| `reference_ner.jsonl` | `{"id","ids"}` for the `lang == "en"` prompts: `[CLS]` (101) … `[SEP]` (102) |

- Reference ids are **not truncated** (the bert `tokenizer.json` truncates at 512; the
  reference disables it). `hftok.Truncate` is tested separately.
- Invisible characters, separators and controls (Cc, Cf, Co, Zl, Zp, and Zs other than U+0020)
  are written as JSON `\uXXXX` escapes. Everything else is literal UTF-8.

## Categories

| Category | Prompts | of which `en` | What it exercises |
|---|---|---|---|
| `en` | 30 | 30 | English, URLs, curly quotes, literal special tokens, U+2581 |
| `code` | 25 | 25 | source code, data formats, literal `<s>…</s>` |
| `ko` | 20 | 0 | Korean, jamo, mixed Latin |
| `vi` | 20 | 0 | Vietnamese with stacked diacritics |
| `cjk` | 25 | 0 | Chinese, Japanese, CJK Extension B+ |
| `ar` | 15 | 0 | Arabic, harakat, ligatures |
| `emoji` | 30 | 25 | ZWJ, flags, keycaps, tags, variation selectors |
| `zero_width` | 35 | 14 | zero-width and invisible characters in EN/KO/VI/ZH |
| `nfkc` | 40 | 29 | compatibility forms, combining marks, Zalgo |
| `whitespace` | 30 | 27 | Unicode spaces, line breaks, controls, empty text |
| `mixed` | 15 | 0 | other scripts, code-switching |
| `long` | 15 | 8 | over 512 XLM-R tokens (595 to 1,842 ids) |

`lang` values: `en`, `ko`, `vi`, `zh`, `ja`, `ar`, `de`, `fr`, `es`, `ru`, `el`, `he`, `th`,
`hi`, `tr`, or `mul` (multilingual). Code with English identifiers and comments is `en`.

## Sources

Every text was written for this project, or is a common phrase or proverb (for example "The
quick brown fox…", "Ăn quả nhớ kẻ trồng cây"), or is a short factual statement. No dataset,
licensed or not, was used.

The corpus is generated deterministically (no randomness, no network) by `build_corpus()` in
`research/src/triage_research/reference_tokenize.py`. A research test fails if the committed
`corpus.jsonl` and the generator disagree.

## Regenerating

```sh
make bootstrap                      # pinned, SHA-verified tokenizer.json files
cd research
uv run python -m triage_research.reference_tokenize corpus     # corpus.jsonl
uv run python -m triage_research.reference_tokenize reference  # reference_*.jsonl
uv run python -m triage_research.reference_tokenize tables     # internal/platform/hftok/tables.go
```

- `reference` reads `TRIAGE_TOKENIZER_DIR` (default `~/.cache/rayld/triage/tokenizers`, layout
  `{bert,ner}/tokenizer.json`).
- It uses `tokenizers==0.23.3`, pinned in `research/pyproject.toml`.
- The Go test `TestTokenizer_Encode_MatchesReference` reads the same directory. It fails, and
  does not skip, when the files are missing.
