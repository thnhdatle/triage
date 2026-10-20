# Fallback, retries and stream continuation

Configure what Triage does when an upstream attempt fails.

Failed attempts move to the next target of the route's fallback chain, within a retry budget.
Privacy constraints hold in every attempt. Final error codes are in [errors.md](errors.md).

## The chain

The chain is the eligible targets of the chosen tier
([target selection](routing-strategies.md#target-selection)), then the route's `fallback`
entries:

```yaml
routes:
  - name: triage-auto
    pool: [local-small, cloud-strong]
    fallback: [tier:strong, tier:weak] # after the chosen tier fails, try strong, then weak
```

Entries are target names (in the pool) or `tier:weak` / `tier:strong`. The chain repeats until
the attempt budget is spent, so a single target is still retried.

## What triggers a fallback

| Upstream outcome | Next |
|---|---|
| 429 (rate limit) | fallback |
| 5xx, connection error, malformed upstream answer | fallback |
| 408, 504, or Triage's own attempt timeout | fallback |
| context length error (prompt too long) | no fallback, unless `context_length_fallback` |
| any other 4xx, including 401 and 404 | never retried: `400 upstream_bad_request` |

## Retry budget and timeouts

```yaml
dispatch:
  max_attempts: 3          # attempts sent per request, including the first (default: 3)
  attempt_timeout: 60s     # per attempt (default: 60s)
  total_timeout: 120s      # per request, backoff waits included (default: 120s)
  backoff:
    base: 100ms            # delay before the first retry of a target (default: 100ms)
    max: 2s                # cap (default: 2s)
  max_retry_after: 10s     # longest upstream Retry-After honoured (default: 10s)
  stream_failure: continue # continue | error (default: continue)
  context_length_fallback: false
```

- For streams, `attempt_timeout` is the time to the first chunk and between chunks.
- Set `targets[].attempt_timeout` for a slow target, such as a local model.
- `total_timeout` ending the request gives `504 upstream_timeout`.
- A longer `Retry-After` than `max_retry_after` drops that target for the request.

The decision lists every attempt with its status.

## Streams

A stream that fails before any content reaches the client is retried silently.

After content was sent, `dispatch.stream_failure` decides.

### `continue` (default)

The next target is asked to continue the answer. The client sees one stream. The text may
overlap or restart at the join.

No continuation inside a tool call, with `n > 1`, or when no target or budget is left. The
stream then ends with an error event.

### `error`

The stream ends with one SSE error event:

```text
data: {"error": {"message": "...", "type": "server_error", "param": null, "code": "upstream_unavailable"}}
```

A stream that ends with `data: [DONE]` is complete.

### Headers and usage on streams

`X-Triage-Target` names the first target only. Continuations are in the decision:

```sh
curl -s http://127.0.0.1:9091/v1/routing-decisions/$DECISION_ID | jq '.attempts[] | {target, status, continuation, masked}'
```

Attempt statuses: `ok`, `cut`, `timeout`, `rate_limited`, `unavailable`, `server_error`,
`bad_request`, `context_length`, `canceled`, `mask_leak`, `rescan_error`,
`rescan_incomplete`.

Usage (with `stream_options.include_usage`) and cost sum all attempts.

## Context length

A too-long prompt gives `400 context_length_exceeded` and is not retried. With
`dispatch.context_length_fallback: true` it is retried once on the next larger window.

## Privacy during fallback

- A request that must stay local never goes to a cloud target. If no local target is left:
  `503 privacy_no_local_target`.
- In `mask` mode, cloud attempts get the masked request; local attempts get the original.
- A continuation to a cloud target is re-scanned and masked. If that fails, the target is
  refused (`rescan_incomplete`, `rescan_error` or `mask_leak`).

See [privacy.md](privacy.md).

## Embeddings

`/v1/embeddings` ignores `fallback` and rules. Embedding targets are tried in order, once
each, up to `dispatch.max_attempts`, without backoff. Requests with findings in `mask` or
`local_only` mode use local targets only.

## Observing fallbacks

- `fallback:<cause>` in the decision's `reasons`, e.g. `fallback:429`, `fallback:timeout`.
- `triage_fallback_attempts_total{route,reason}`.
- `triage_target_healthy{target}` (see [health probes](operations.md#health-probes)).
