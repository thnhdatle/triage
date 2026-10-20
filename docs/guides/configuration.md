# Configuration

Write and check a Triage configuration file.

Triage reads one YAML file from `--config` or `$TRIAGE_CONFIG`. Every key has a default unless
marked **required**. [`deploy/config.example.yaml`](../../deploy/config.example.yaml) shows
every key; [`deploy/config.quickstart.yaml`](../../deploy/config.quickstart.yaml) is a minimal
file.

```sh
triage config validate --config triage.yaml        # "ok", or every problem (exit 2)
triage config print --redact --config triage.yaml  # the file with defaults filled in
```

- Unknown keys and multi-document files are errors.
- **Durations**: `"250ms"`, `"30s"`, `"2m"`, `"14d"`, `"1d12h"`.
- **Sizes**: bytes, or `B`, `KB`, `MB`, `GB` (1000) and `KiB`, `MiB`, `GiB` (1024), e.g. `"8MiB"`.
- Never put secrets in the file. Use `api_key_env` / `api_key_file` and `auth.keys_file` /
  `auth.keys_env`.
- `~` at the start of a path is the home directory.
- `targets` and `routes` need at least one entry.

Most keys apply on [reload](operations.md#hot-reload). `server`, `admin.listen`,
`auth.enabled`, `onnx.library_path`, `decisions.buffer_size` and `tracing.capture_content`
need a restart.

## server, admin, auth, api

| Key | Type | Default | Description |
|---|---|---|---|
| `server.listen` | `host:port` | `127.0.0.1:8080` | Data-plane address. |
| `server.allow_unauthenticated` | bool | `false` | Needed for a non-loopback address without auth. |
| `admin.listen` | `host:port` | `127.0.0.1:9091` | Admin address. No authentication; keep it private. |
| `auth.enabled` | bool | `true` | Require `Authorization: Bearer <key>`. |
| `auth.keys_file` | path | unset | One key per line, `#` comments. Exclusive with `keys_env`. |
| `auth.keys_env` | env var name | unset | Comma-separated keys. Exclusive with `keys_file`. |
| `api.max_body_bytes` | size | `8MiB` | Body limit (`413 request_too_large`). Above 0. |
| `api.expose_targets` | bool | `false` | List targets too in `GET /v1/models`. |
| `api.expose_decision_detail` | bool | `false` | Add `X-Triage-Route-Reason` to non-streamed responses. |

With `auth.enabled: true`, set exactly one of `keys_file` and `keys_env`, with at least one key.

## onnx, models

| Key | Type | Default | Description |
|---|---|---|---|
| `onnx.library_path` | path | empty | ONNX Runtime file or directory, first in the [lookup order](operations.md#onnx-runtime-library). |
| `models.cache_dir` | path | `~/.cache/rayld/triage/models` | Model files. |

## providers

A map from a name you choose to an upstream. At least one is required.

| Key | Type | Default | Description |
|---|---|---|---|
| `providers.<name>.type` | `openaicompat` or `anthropic` | required | `openaicompat`: LM Studio, vLLM, OpenAI, ... |
| `providers.<name>.base_url` | URL | required for `openaicompat` | Includes the version path, e.g. `http://localhost:1234/v1`. `anthropic` default: `https://api.anthropic.com/v1`. |
| `providers.<name>.api_key_env` | env var name | unset | Variable with the key; must not be empty. Exclusive with `api_key_file`. |
| `providers.<name>.api_key_file` | path | unset | File with the key. Exclusive with `api_key_env`. |
| `providers.<name>.max_idle_conns_per_host` | int | `0` | Idle connections per host; `0` is Go's default of 2. |

`openaicompat` works without a key (LM Studio). `anthropic` cannot serve embeddings.

## health

Read [operations.md](operations.md#health-probes) before choosing `completion`.

| Key | Type | Default | Description |
|---|---|---|---|
| `health.probe` | `models` or `completion` | `models` | `completion` sends real requests; forces loads on LM Studio. |
| `health.interval` | duration | `10s` | Probe period. |
| `health.failure_threshold` | int | `3` | Failures before unhealthy. |
| `health.cooldown` | duration | `30s` | Time an unhealthy target is skipped. |

## selection

| Key | Type | Default | Description |
|---|---|---|---|
| `selection.mode` | `priority`, `round_robin` or `least_inflight` | `priority` | Order of targets in a tier. |
| `selection.tokenizer` | `estimate`, `bert` or a path | `estimate` | Token counting for windows, rules and `length`. |

### `selection.tokenizer`

| Value | Counts with | Needs |
|---|---|---|
| `estimate` | `ceil(ascii_bytes / 4 + non_ascii_bytes / 3)` | nothing |
| `bert` | the XLM-R tokenizer, exactly | `triage models pull bert` |
| a path to a `tokenizer.json` | that tokenizer, exactly | the file |

- `estimate` can be off either way, more for CJK, Korean, Vietnamese and code. Set a target's
  `context_window` below the real limit, or use an exact tokenizer.
- Only Unigram and WordPiece tokenizers are supported. Others fail validation.
- `bert` and `mf` strategies ignore this key.

## targets

A target is a provider and model with routing attributes.

| Key | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Unique name. |
| `provider` | string | required | A key of `providers`. |
| `model` | string | required | Model id sent upstream. |
| `locality` | `local` or `cloud` | required | Used by privacy modes. |
| `tier` | `weak` or `strong` | required | Empty for embedding targets. |
| `context_window` | int | required | Prompt plus output tokens. Above 0 (embedding targets: 0 or more). |
| `capabilities` | list of `tools`, `vision`, `json_mode`, `embeddings` | empty | Requests needing one go only to targets that list it. `embeddings` stands alone. |
| `extra_headers` | map string to string | none | Headers sent to this target. Hidden when printed. |
| `attempt_timeout` | duration | unset | Overrides `dispatch.attempt_timeout`. |
| `pricing.input_per_mtok` | float | `0` | USD per million uncached input tokens. |
| `pricing.output_per_mtok` | float | `0` | USD per million output tokens. |
| `pricing.cached_input_per_mtok` | float | `0` | USD per million cached input tokens. |

## routes

A route is a virtual model, chosen by the request's `model`.

| Key | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Unique; clients send it as `model`. |
| `pool` | list of target names | all targets | Allowed targets, in priority order. |
| `strategy` | `none`, `random`, `length`, `bert` or `mf` | `none` | See [routing-strategies.md](routing-strategies.md). |
| `threshold` | float in [0, 1] | `0.5` | `p_strong` at or above goes to `strong`. `0` is valid. |
| `default_tier` | `weak` or `strong` | `weak` | Tier with `strategy: none` and no rule match. |
| `on_tier_unavailable` | `escalate`, `degrade` or `fail` | `escalate` | When the tier has no eligible target. |
| `fallback` | list of target names or `tier:weak` / `tier:strong` | empty | See [fallback.md](fallback.md). Names must be in `pool`. |
| `privacy.mode` | `off`, `mask` or `local_only` | `mask` | See [privacy.md](privacy.md). |
| `privacy.max_client_mode` | `off`, `mask` or `local_only` | `local_only` | Strictest `X-Triage-Privacy` allowed. At least as strict as `mode`. |
| `rules.allow_client_override` | bool | `false` | Honour `X-Triage-Tier`. |
| `rules.match` | list | empty | Rules before the strategy; first match wins. |

Each `rules.match` entry has a `tier` (required) and exactly one of:

| Key | Type | Description |
|---|---|---|
| `min_prompt_tokens` | int above 0 | Prompt tokens at or above it. |
| `last_user_regex` | regular expression | Go (RE2), on the last user message. |

## privacy

| Key | Type | Default | Description |
|---|---|---|---|
| `privacy.scan_system` | bool | `false` | Also scan system messages. |
| `privacy.max_scan_bytes` | size | `256KiB` | Above this, `on_oversize` applies. |
| `privacy.on_oversize` | `local_only` or `reject` | `local_only` | Keep local, or `413 privacy_scan_limit`. |
| `privacy.detectors.<id>` | bool | all `true`, `street_address` `false` | Toggle a detector: `credit_card`, `iban`, `email`, `ip`, `us_ssn`, `secret`, `kr_rrn`, `phone`, `vn_cccd`, `street_address`. |
| `privacy.ner.enabled` | bool | `false` | English NER for names. Needs `triage models pull ner` and ONNX Runtime. |
| `privacy.ner.min_score` | float in [0, 1] | `0.8` | Minimum entity score. |
| `privacy.ner.max_tokens` | int | `2048` | Longer text is `ner_unavailable` (kept local). |
| `privacy.ner.timeout` | duration | `200ms` | Scan limit; exceeding it is `ner_unavailable`. |

## difficulty

| Key | Type | Default | Description |
|---|---|---|---|
| `difficulty.timeout` | duration | `50ms` | Scoring limit for `random` and `length`. |
| `difficulty.on_error` | `weak` or `strong` | `strong` | Tier when scoring fails. |
| `difficulty.missing_artifact` | `error` or `fallback` | `error` | `fallback`: use `length` when model files are missing. |
| `difficulty.length.midpoint_tokens` | int | `2048` | Tokens with `p_strong` 0.5. |
| `difficulty.length.slope` | float | `0.004` | Logistic slope per token. |
| `difficulty.bert.variant` | `auto`, `int8` or `fp32` | `auto` | `auto`: INT8 if built, else FP32. |
| `difficulty.bert.timeout` | duration | `300ms` | Scoring limit. |
| `difficulty.bert.max_input_tokens` | int | `512` | Input cut; 2 to 512. |
| `difficulty.bert.intra_op_threads` | int | `4` | Threads per inference. |
| `difficulty.bert.max_concurrency` | int | `0` | Parallel inferences; `0` = physical cores / threads. |
| `difficulty.bert.queue_size` | int | `64` | Waiting requests before rejecting. |
| `difficulty.mf.allow_external_embedding` | bool | `false` | Required for `mf`: sends text to the embedding provider. |
| `difficulty.mf.provider` | provider name | `openai` | `openaicompat` with an API key. |
| `difficulty.mf.timeout` | duration | `1s` | Scoring limit. |
| `difficulty.mf.fallback` | `bert` or `length` | `bert` | Strategy for requests that must not leave. |
| `difficulty.mf.cache_size` | int | `10000` | Embedding cache entries; `0` disables. |

## dispatch

See [fallback.md](fallback.md).

| Key | Type | Default | Description |
|---|---|---|---|
| `dispatch.max_attempts` | int | `3` | Attempts per request, first included. |
| `dispatch.attempt_timeout` | duration | `60s` | Per attempt (streams: per chunk gap). |
| `dispatch.total_timeout` | duration | `120s` | Per request. At least `attempt_timeout`. |
| `dispatch.backoff.base` | duration | `100ms` | First retry delay; doubles. |
| `dispatch.backoff.max` | duration | `2s` | Delay cap. |
| `dispatch.max_retry_after` | duration | `10s` | Longest `Retry-After` honoured. |
| `dispatch.stream_failure` | `continue` or `error` | `continue` | After content was sent. |
| `dispatch.context_length_fallback` | bool | `false` | Retry once on a larger window. |

## decisions, tracing

| Key | Type | Default | Description |
|---|---|---|---|
| `decisions.buffer_size` | int | `10000` | Decisions kept in memory. |
| `tracing.capture_content` | bool | `false` | Put prompt and response text on spans. Restart-only. |

**`tracing.capture_content` records the original text of local attempts.** Keep it off unless
your trace backend may hold prompts. Turn tracing on with `OTEL_*` variables, see
[operations.md](operations.md#tracing).

## Cross-checks

`triage config validate` also checks that:

- `pool` and `fallback` names are targets (`fallback` names in the pool);
- providers exist and names are unique;
- `embeddings` targets have no tier and no `anthropic` provider;
- model files for `bert` and `mf` are pulled (or `missing_artifact: fallback`);
- `mf` routes have `allow_external_embedding: true` and a provider with a set API key;
- `privacy.ner.enabled` has the `ner` files and ONNX Runtime.

Auth keys are checked at serve and reload, not by `config validate`.
