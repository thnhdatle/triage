# Error codes

Look up an error returned by Triage.

Errors use the OpenAI error shape, on the data plane and the admin API:

```json
{"error": {"message": "unknown model \"gpt-9\"", "type": "invalid_request_error", "param": "model", "code": "model_not_found"}}
```

- `code` is stable. `message` may change.
- `param` names the bad field, else `null`.
- `type` follows the status: `authentication_error` (401), `rate_limit_error` (429),
  `server_error` (5xx), `invalid_request_error` (other 4xx).

Every response carries `X-Request-Id`. Most also carry `X-Triage-Decision`: look it up with
`GET /v1/routing-decisions/{id}` on the admin listener.

## Codes

| Code | HTTP | Meaning |
|---|---|---|
| `invalid_request` | 400 | Bad JSON or field; `param` names it. |
| `invalid_api_key` | 401 | Bearer key missing or unknown. |
| `model_not_found` | 404 | No such route. `GET /v1/models` lists them. |
| `request_too_large` | 413 | Body above `api.max_body_bytes` (default 8 MiB). |
| `privacy_scan_limit` | 413 | Above the privacy scan limit; oversize policy rejects. |
| `privacy_mode_not_allowed` | 400 | `X-Triage-Privacy` not allowed (`privacy.max_client_mode`). |
| `privacy_no_local_target` | 503 | Must stay local; no healthy local target. Never sent to cloud. See [privacy.md](privacy.md). |
| `no_eligible_target` | 503 | No healthy target fits, or every chain entry was refused. |
| `context_length_exceeded` | 400 | Upstream says prompt too long. |
| `upstream_bad_request` | 400 | Upstream 4xx (not 429, 408, context length), e.g. wrong provider key. Never retried. |
| `rate_limit_exceeded` | 429 | Upstream rate limit; fallback chain exhausted. |
| `upstream_timeout` | 504 | Attempt or total budget timed out. |
| `upstream_unavailable` | 502 | Upstream unreachable or 5xx on every attempt. |
| `request_canceled` | 499 | Client closed the connection. |
| `internal_error` | 500 | Bug; details in the server log under the request id. |
| `method_not_allowed` | 405 | Wrong method; see `Allow`. |
| `not_found` | 404 | No such endpoint. |

Admin API only:

| Code | HTTP | Meaning |
|---|---|---|
| `invalid_cursor` | 400 | Malformed `cursor`. |
| `decision_not_found` | 404 | Unknown id, or evicted (`decisions.buffer_size`). |
| `config_invalid` | 422 | Reload rejected; previous config stays active. |

## Streaming

Once a stream has started, an error arrives as one SSE event with the same body. The stream
then ends without `data: [DONE]`:

```text
data: {"error": {"message": "...", "type": "server_error", "param": null, "code": "upstream_unavailable"}}
```

A stream that ends with `data: [DONE]` is complete.

## Fallback and retries

`rate_limit_exceeded`, `upstream_timeout` and `upstream_unavailable` come only after retries
and fallbacks are used up. The decision lists every attempt. See [fallback.md](fallback.md).
