# Routing strategies

Choose how a route sends each request to the `weak` or `strong` tier.

Privacy runs first ([privacy.md](privacy.md)); failures are handled by
[fallback.md](fallback.md).

## Pipeline

1. The request's `model` picks the route (`404 model_not_found` if none).
2. Privacy may restrict the request to local targets.
3. A matching **rule** sets the tier and skips the strategy.
4. Otherwise the **strategy** scores the request and the **threshold** picks the tier.
5. A target of that tier is chosen ([Target selection](#target-selection)).
6. The request follows the fallback chain.

Rules and strategies never loosen privacy. The decision
(`GET /v1/routing-decisions/{id}`) shows the reasons, score and tier. With
`api.expose_decision_detail: true`, `X-Triage-Route-Reason` shows them too, e.g.
`strategy:bert p=0.71>=0.62`.

## Strategies

```yaml
routes:
  - name: triage-auto
    pool: [local-small, cloud-strong]
    strategy: length   # none | random | length | bert | mf (default: none)
    threshold: 0.5     # p_strong at or above this routes to strong, in [0, 1] (default: 0.5)
    default_tier: weak # used with strategy none when no rule matches (default: weak)
```

A strategy returns `p_strong` in [0, 1]. The request goes to `strong` when
`p_strong >= threshold`.

| Strategy | Needs | Scores |
|---|---|---|
| `none` | nothing | no scoring; rules, else `default_tier` |
| `random` | nothing | uniform, stable per request id; a baseline |
| `length` | nothing | logistic over prompt tokens |
| `bert` | `triage models pull bert`, ONNX Runtime | pretrained BERT classifier, local |
| `mf` | `triage models pull mf`, an OpenAI API key, opt-in | pretrained matrix factorization; **sends text off the machine** |

`bert` and `mf` score only the last `user` message. A request without one follows
`difficulty.on_error`.

### length

```yaml
difficulty:
  length:
    midpoint_tokens: 2048 # token count with p_strong 0.5 (default: 2048)
    slope: 0.004          # logistic slope per token (default: 0.004)
```

Counts tokens of all messages with `selection.tokenizer`
([configuration.md](configuration.md#selection)). Start here: no downloads, the quickstart
uses it.

### bert

The pretrained classifier `routellm/bert_gpt4_augmented`, run locally on ONNX Runtime.

```sh
triage models pull bert --int8   # see models.md
```

```yaml
difficulty:
  bert:
    variant: auto         # auto (INT8 if built, else FP32) | int8 | fp32 (default: auto)
    timeout: 300ms        # scoring time limit (default: 300ms)
    max_input_tokens: 512 # 2 to 512 (default: 512)
    intra_op_threads: 4   # ONNX Runtime threads per inference (default: 4)
    max_concurrency: 0    # parallel inferences; 0 = automatic (default: 0)
    queue_size: 64        # waiting requests before rejecting (default: 64)
```

- A full queue or a timeout follows `difficulty.on_error`, counted in
  `triage_scoring_rejected_total{strategy="bert"}`.
- If that rises under load, raise `max_concurrency` (and CPU) or the timeout.
- **FP32 can exceed the 300 ms default**; raise `bert.timeout` for `variant: fp32`.

The checkpoint was trained for GPT-4 against Mixtral. For other pairs,
[calibrate the threshold](#calibrating-the-threshold). See
[models.md](models.md#licenses-and-provenance).

### mf

The pretrained matrix-factorization router. **It sends the scored text to an embedding API.**
Read [privacy.md](privacy.md#mf-egress) first.

```yaml
difficulty:
  mf:
    allow_external_embedding: true # required; without it a route using mf is rejected
    provider: openai               # a provider of type openaicompat with an API key
    timeout: 1s                    # scoring time limit (default: 1s)
    fallback: bert                 # bert if its artifacts exist, else length (default: bert)
    cache_size: 10000              # embedding cache entries (default: 10000)
```

- Requests with findings use `difficulty.mf.fallback`; nothing is sent.
- Add a target with that provider and model `text-embedding-3-small` to record the
  embedding cost as `scoring_cost_usd`. Otherwise it is 0.

### Errors, timeouts and missing artifacts

```yaml
difficulty:
  timeout: 50ms           # scoring time limit for random and length (default: 50ms)
  on_error: strong        # tier used when scoring fails: weak | strong (default: strong)
  missing_artifact: error # error | fallback (default: error)
```

- **`on_error`**: tier when scoring fails. The reason names the cause, e.g.
  `strategy:bert error->strong(timeout)`.
- **`missing_artifact: fallback`**: use `length` when `bert` or `mf` files are missing. It
  never relaxes the `mf` egress rules.

## Rules

Rules run before the strategy. The first match wins.

```yaml
routes:
  - name: triage-auto
    rules:
      allow_client_override: true # honour the X-Triage-Tier header (default: false)
      match:                      # in order, first match wins
        - min_prompt_tokens: 16000
          tier: strong
        - last_user_regex: "(?i)\\b(prove|derive)\\b"
          tier: strong
```

1. `X-Triage-Tier: weak` or `strong`, only with `allow_client_override: true`
   (`rule:header`).
2. `match` entries in order: `min_prompt_tokens` (`rule:length`) or `last_user_regex`, a Go
   RE2 regex on the last user message (`rule:regex`).
3. No match: the strategy, or `default_tier`.

## Target selection

The route picks targets of the tier that are:

- healthy ([operations.md](operations.md#health-probes));
- allowed by privacy;
- large enough for the prompt plus `max_tokens`;
- capable of what the request needs (`tools`, `vision`, `json_mode`).

`selection.mode` orders them: `priority` (default), `round_robin` or `least_inflight`.

If the tier has no eligible target, `on_tier_unavailable` decides:

| Value | Effect |
|---|---|
| `escalate` (default) | `weak` uses `strong`; `strong` fails |
| `degrade` | `strong` uses `weak`; `weak` fails |
| `fail` | the request fails |

It never relaxes locality.

## Calibrating the threshold

Find the threshold that sends a chosen share of your traffic to strong:

```sh
triage calibrate --config triage.yaml --route triage-auto \
  --strong-ratio 0.3 --sample sample.jsonl
```

| Flag | Meaning |
|---|---|
| `--config` | configuration file (default `$TRIAGE_CONFIG`) |
| `--route` | route to calibrate (required) |
| `--strong-ratio` | target share sent to strong, in [0, 1] (required) |
| `--sample` | JSONL, one request per line (required) |

```json
{"id": "q1", "messages": [{"role": "user", "content": "What is 17 * 23?"}]}
{"id": "q2", "prompt": "Prove that the square root of 2 is irrational."}
```

It prints a snippet. Copy `threshold` into the route:

```text
strategy bert on 500 requests: threshold 0.6213 gives strong ratio 0.300 (target 0.300)
routes:
  - name: triage-auto
    threshold: 0.6213
```

- Rules and privacy are not applied, so live ratios differ.
- **With `mf`, every sample line is sent to the embedding provider, unscanned.** Use a sample
  you may send.

To measure quality too, see [evaluation.md](evaluation.md).
