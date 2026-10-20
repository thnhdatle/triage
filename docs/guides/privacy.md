# Privacy

Keep personal data in requests away from cloud targets.

Triage scans each request. Depending on the route's mode, it masks values for cloud targets
or keeps the request on local targets.

**Protection covers detectable formats only.** Names and locations are found only by the
optional NER detector, which is **English only** and off by default. "my neighbour John Smith
in Hanoi" passes the regex detectors unchanged. Images are never scanned.

## Modes

Strictness: `off` < `mask` < `local_only`.

| Mode | Detectors run | Request with findings |
|---|---|---|
| `off` | no (except on `mf` routes, see [mf egress](#mf-egress)) | sent as is |
| `mask` (default) | yes | cloud gets a masked copy, local gets the original |
| `local_only` | yes | local targets only, fallbacks included |

```yaml
routes:
  - name: triage-auto
    pool: [local-small, cloud-strong]
    privacy:
      mode: mask                  # off | mask | local_only (default: mask)
      max_client_mode: local_only # strictest mode a client may ask for (default: local_only)
```

A request without findings can go to any target. A request that must stay local and has no
healthy local target gets `503 privacy_no_local_target`. It is never sent to a cloud target.

### Fail-closed cases

These keep the request local, even in `mask` mode:

- text larger than `privacy.max_scan_bytes` with `on_oversize: local_only` (default);
- NER could not scan the text (`ner_unavailable`, see [NER](#ner-english-only));
- a detector error (`privacy:scan_error`).

### Client header `X-Triage-Privacy`

```sh
curl -s http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer '"$TRIAGE_KEY" \
  -H 'X-Triage-Privacy: local_only' \
  -H 'Content-Type: application/json' \
  -d '{"model":"triage-auto","messages":[{"role":"user","content":"Summarise this."}]}'
```

| Header value compared with the route's `mode` | Result |
|---|---|
| same or looser | ignored |
| stricter, and not stricter than `max_client_mode` | applies |
| stricter than `max_client_mode` | `400 privacy_mode_not_allowed` |
| not `off`, `mask` or `local_only` | `400 privacy_mode_not_allowed` |

A client can never loosen privacy.

## Detectors

Formats and context words are in [`testdata/pii/README.md`](../../testdata/pii/README.md).
Precision and recall are in `testdata/pii/REPORT.md`.

| Type (placeholder `TYPE`) | Class | Finds |
|---|---|---|
| `credit_card` (`CREDIT_CARD`) | checksum | card numbers, Luhn valid |
| `iban` (`IBAN`) | checksum | IBANs, mod-97 valid |
| `email` (`EMAIL`) | structural | `local@domain.tld` |
| `ip` (`IP`) | structural | IPv4 and IPv6 |
| `us_ssn` (`US_SSN`) | structural | `AAA-GG-SSSS` |
| `secret` (`SECRET`) | structural | `sk-`, `AKIA`, `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_`, `xox[abprs]-` keys, JWTs |
| `kr_rrn` (`KR_RRN`) | structural | Korean resident registration number |
| `phone` (`PHONE`) | context-anchored | E.164; Korean, Vietnamese, US with a context word |
| `vn_cccd` (`VN_CCCD`) | context-anchored | Vietnamese citizen ID |
| `street_address` (`STREET_ADDRESS`) | heuristic, **off by default** | English, Vietnamese, Korean addresses |
| `person`, `location`, `organization` | NER, **off by default** | see [NER](#ner-english-only) |

Context-anchored detectors need a context word (such as `phone` or `CCCD`) within 32
characters. A bare local phone number is not detected.

```yaml
privacy:
  detectors:
    street_address: true   # enable the address heuristic
```

## What is scanned

| Scanned | Not scanned |
|---|---|
| text and tool-call arguments of all non-system roles | image parts |
| system messages, only with `privacy.scan_system: true` | tool definitions, `name` fields, other fields |

A value split across two parts is not detected. Once a value is found, every occurrence is
masked, including in system messages.

### Size limit

Above `privacy.max_scan_bytes` (default 256 KiB), nothing is scanned:

| `on_oversize` | Effect |
|---|---|
| `local_only` (default) | treated as having findings: local only |
| `reject` | `413 privacy_scan_limit` |

## Masking

The mapping lives in memory for one request. It is never logged or stored.

### Placeholder format

```text
[[TYPE_n]]        e.g. [[EMAIL_1]], [[CREDIT_CARD_2]]
```

| Original | Sent to a cloud target |
|---|---|
| `Mail ana@example.com or call +84 912 345 678` | `Mail [[EMAIL_1]] or call [[PHONE_1]]` |

### Per attempt, by locality

Cloud attempts get the masked request. Local attempts get the original. If a masked request
still contains a value, that cloud target is refused (`mask_leak`).

### Continuation re-scan

A stream continued on a cloud target has its partial answer re-scanned and masked. See
[fallback.md](fallback.md).

### Unmask

Output of a masked attempt is restored before it reaches the client, streamed or not,
including tool-call `arguments`.

### Partial unmask

A model may alter a placeholder (`[[email_1]]`, `[EMAIL_1]`). Such output is passed through
unchanged and flagged:

- header `X-Triage-Unmask: partial` (non-streamed);
- SSE comment `: triage-unmask=partial` (streamed);
- `unmask: partial` in the decision;
- `triage_unmask_failures_total{route}`.

## NER (English only)

Finds person, location and organization names with `dslim/bert-base-NER`.
**English only.**

```sh
triage models pull ner
```

```yaml
privacy:
  ner:
    enabled: true
    min_score: 0.8   # entity score threshold, [0, 1]
    max_tokens: 2048 # longer text counts as ner_unavailable
    timeout: 200ms   # scan time limit, queueing included
```

NER is fail-closed: too long, too slow or an error gives `ner_unavailable`, which keeps the
request local. If `triage_privacy_findings_total{type="ner_unavailable"}` is high, raise
`max_tokens` or `timeout`.

NER needs [ONNX Runtime](operations.md#onnx-runtime-library). A `noonnx` build rejects it.

## mf egress

**The `mf` strategy sends prompt text off the machine** to score it
([routing-strategies.md](routing-strategies.md#mf)). It needs
`difficulty.mf.allow_external_embedding: true`.

What is sent, to `POST {base_url}/embeddings` of `difficulty.mf.provider`:

- `model`: `text-embedding-3-small`;
- `input`: the text of the **last user message**, cut to about 8,000 tokens;
- `dimensions`: 1536;
- the API key, `extra_headers` of a matching target, and `traceparent`.

Nothing else is sent. Cached results send nothing. Calls are counted in
`triage_mf_external_embeddings_total`.

- Detectors always run on `mf` routes, even in `off` mode.
- A request with any finding, oversize text or scan error uses `difficulty.mf.fallback`
  instead, and nothing is sent.
- **Undetectable data, such as a name without NER, is still sent.**

The embedding cost is `scoring_cost_usd` in the decision.

## What is recorded

Decisions, metrics and spans hold finding counts by type, never values or prompt text. See
[operations.md](operations.md#metrics).
