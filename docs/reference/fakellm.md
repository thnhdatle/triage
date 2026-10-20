# fakellm reference

Use `fakellm`, a fake OpenAI-compatible provider, as the upstream in tests, benchmarks and
examples.

It runs no model. Answers are deterministic, or set by request headers or a per-model script.
It serves chat completions, embeddings and a model list. It does not implement the Anthropic
Messages API.

> **Tests only.** fakellm has no authentication and logs request headers, including
> `Authorization`. Do not expose it on an untrusted network.

## Running it

### Container image

```bash
docker run --rm -p 8090:8090 ghcr.io/thnhdatle/fakellm:v0.1.0                  # start
curl -s localhost:8090/healthz                                                  # ok
docker run --rm -p 8090:8090 ghcr.io/thnhdatle/fakellm:v0.1.0 --record-limit 500  # flags after the image
```

### Release archives

`fakellm_<version>_<os>_<arch>.tar.gz` for Linux `amd64` and `arm64`. Checksums are in
`checksums.txt`.

```bash
tar -xzf fakellm_0.1.0_linux_amd64.tar.gz fakellm
./fakellm --listen 127.0.0.1:8090
```

### From source

```bash
go run ./cmd/fakellm                     # listens on :8090
make docker                              # builds thnhdatle/fakellm:dev for the host arch
docker run --rm -p 8090:8090 thnhdatle/fakellm:dev
```

[`deploy/docker-compose.yml`](../../deploy/docker-compose.yml) runs fakellm for the quickstart.

### Flags

```
fakellm [--listen :8090] [--record=true] [--record-limit 10000]
fakellm healthcheck [--listen :8090]
```

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `:8090` | Listen address. |
| `--record` | `true` | Keep the request log. |
| `--record-limit` | `10000` | Log size; at least 1. |

Exit status: `0` clean shutdown, `1` serve error, `2` invalid flags.

### `healthcheck` subcommand

`fakellm healthcheck [--listen ADDR]` exits `0` if `/healthz` answers 200, else `1`. Use it in
images without `curl`.

## Endpoints

| Method and path | Purpose | Success response |
|---|---|---|
| `POST /v1/chat/completions` | Chat completion, plain or SSE | `200` JSON or `text/event-stream` |
| `POST /v1/embeddings` | Embeddings | `200` JSON |
| `GET /v1/models` | `fake-default` and every model seen | `200` JSON |
| `GET /healthz` | Liveness | `200` `ok\n` (`text/plain`) |
| `GET /readyz` | Same as `/healthz` | `200` `ok\n` (`text/plain`) |
| `PUT /_fake/script` | Install a per-model script | `204` |
| `DELETE /_fake/script` | Remove scripts, reset counters | `204` |
| `GET /_fake/requests` | Read the request log | `200` JSON |
| `DELETE /_fake/requests` | Clear the request log | `204` |

Bodies are limited to 32 MiB (1 MiB for `PUT /_fake/script`). At most 10000 distinct model
names; beyond that, `400 too many distinct models`.

### `POST /v1/chat/completions`

| Field | Use |
|---|---|
| `model` | Default `fake-default`. Selects script and counter. |
| `messages[].role`, `messages[].content` | String or text parts. |
| `stream` | `true` for SSE. |
| `stream_options.include_usage` | Adds a final usage chunk. |
| `tools[].function.name` | First tool, for `tool-call`. |

Other fields are ignored.

```json
{
  "id": "chatcmpl-1f3a9c0d5e7b2a46",
  "object": "chat.completion",
  "created": 1760000000,
  "model": "m1",
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": "cedar onyx alpha iris flint meadow"}, "finish_reason": "stop"}
  ],
  "usage": {"prompt_tokens": 3, "completion_tokens": 6, "total_tokens": 9}
}
```

`id` is stable across runs. It restarts after `DELETE /_fake/script`.

Streams send content chunks, a finish chunk, an optional usage chunk, then `data: [DONE]`.
Pieces are 16 bytes by default (`X-Fake-Chunk-Bytes`).

In the `tool-call` scenario, the reply calls the first tool with `{"input": <last user
message>}`.

### `POST /v1/embeddings`

| Field | Use |
|---|---|
| `model` | Default `fake-default`. Seeds the vectors. |
| `input` | String or 1 to 4096 strings. Required. |
| `dimensions` | 1 to 16384. Default `1024`. |

The same model, text and dimensions always give the same unit vector. `X-Fake-*` headers
and scripts do not apply.

### `GET /v1/models`

`fake-default` first, then every model seen, sorted.

## Deterministic content

The default reply is six words from a fixed list, derived from the model and messages. Same
input, same reply, on any machine.

## Usage and token counting

Tokens are whitespace-separated words.

| Field | Chat | Embeddings |
|---|---|---|
| `prompt_tokens` | Words in all messages | Words in all inputs |
| `completion_tokens` | Words in the reply or tool arguments | not present |
| `total_tokens` | `prompt_tokens + completion_tokens` | same as `prompt_tokens` |

## Behaviour control: `X-Fake-*` headers

Chat completions only.

| Header | Values | Default | Effect |
|---|---|---|---|
| `X-Fake-Scenario` | a scenario name (below) | `ok`, or the script | Picks the scenario; skips the script. Unknown: `400`. |
| `X-Fake-Answer` | any string; `{n}` and `{user}` are substituted | unset | Reply text. `{n}`: request counter; `{user}`: last user message. |
| `X-Fake-Latency-Ms` | integer ≥ 0 | `0` | Delay in `slow`. |
| `X-Fake-Cut-After` | integer ≥ 0 | `1` | Events before the cut in `cut-stream`. |
| `X-Fake-Chunk-Bytes` | integer ≥ 0 | `16` | Stream piece size (`0` = default). |
| `X-Fake-Chunk-Delay-Ms` | integer ≥ 0 | `0` | Pause between events. |
| `X-Fake-Chunk-Split-Runes` | `true` (case-insensitive) | off | Allow splitting UTF-8 characters. |

A bad integer value returns `400`.

### Scenarios

| Scenario | Non-streaming | Streaming |
|---|---|---|
| `ok` | `200`, deterministic reply (or `X-Fake-Answer`) | same as SSE |
| `echo` | `200`, last user message (`X-Fake-Answer` wins) | same as SSE |
| `tool-call` | `200`, call to the first tool; text if no tools | tool call streamed |
| `slow` | waits `X-Fake-Latency-Ms`, then `ok` | same, then streams |
| `timeout` | never answers (up to 10 minutes), then drops | same |
| `429` | `429`, `Retry-After: 1`, `rate_limit_exceeded` | same JSON error |
| `500` | `500`, `internal_error` | same JSON error |
| `cut-stream` | dropped, no response | sends `X-Fake-Cut-After` events, then drops (no `[DONE]`) |
| `invalid-json` | `200`, truncated JSON | one truncated event, no `[DONE]` |

"Dropped" means the client sees a transport error, not an HTTP status.

```bash
# Rate-limited answer
curl -si localhost:8090/v1/chat/completions -H 'X-Fake-Scenario: 429' \
  -d '{"model":"m1","messages":[{"role":"user","content":"hi"}]}'

# Stream that breaks after three events, 4-byte pieces, 50 ms apart
curl -sN localhost:8090/v1/chat/completions \
  -H 'X-Fake-Scenario: cut-stream' -H 'X-Fake-Cut-After: 3' \
  -H 'X-Fake-Chunk-Bytes: 4' -H 'X-Fake-Chunk-Delay-Ms: 50' \
  -d '{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# Fixed answer with the counter and the user text
curl -s localhost:8090/v1/chat/completions -H 'X-Fake-Answer: reply {n} to {user}' \
  -d '{"model":"m1","messages":[{"role":"user","content":"hello"}]}'
```

## Per-model scripting

A script sets the behaviour of a model's next requests, for clients that cannot send headers.

### `PUT /_fake/script`

| Field | Required | Meaning |
|---|---|---|
| `model` | yes | Model of the rule. |
| `scenario` | yes | A scenario name. |
| `count` | yes | Requests covered, at least 1. |
| `params.answer` | no | Like `X-Fake-Answer`. |
| `params.latency_ms` | no | Like `X-Fake-Latency-Ms`. |
| `params.cut_after` | no | Like `X-Fake-Cut-After`. |
| `params.chunk_bytes` | no | Like `X-Fake-Chunk-Bytes`. |

```bash
curl -s -X PUT localhost:8090/_fake/script -d '[
  {"model":"m1","scenario":"500","count":2},
  {"model":"m1","scenario":"ok","count":1,"params":{"answer":"recovered on try {n}"}},
  {"model":"m2","scenario":"cut-stream","count":1,"params":{"cut_after":2,"chunk_bytes":4}}
]'
# m1: 500, 500, then "recovered on try 3", then deterministic ok replies
# m2: the first request is cut after two events (if streamed), later ones are ok
```

- A new script **replaces** all rules. At most 1000 rules.
- Rules run in order per model. An empty queue behaves as `ok`.
- `X-Fake-Scenario` skips the script for that request. Other headers override `params`.

### `DELETE /_fake/script`

Removes all rules and resets counters. Call it between tests.

## Request log

Chat and embeddings requests are logged, including rejected ones.

### `GET /_fake/requests`

```json
{
  "requests": [
    {
      "method": "POST",
      "path": "/v1/chat/completions",
      "headers": {"Content-Type": ["application/json"], "X-Fake-Scenario": ["echo"]},
      "body": "{\"model\":\"m1\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}",
      "model": "m1",
      "kind": "chat",
      "input_count": 0
    }
  ],
  "dropped": 0
}
```

| Field | Meaning |
|---|---|
| `method`, `path` | Method and path. |
| `headers` | All request headers. |
| `body` | Raw body. |
| `model` | Model; empty for invalid JSON. |
| `kind` | `chat` or `embeddings`. |
| `input_count` | Embedding inputs; `0` for chat. |
| `dropped` | Entries evicted from the full log. |

### `DELETE /_fake/requests`

Empties the log.

```bash
curl -s -X DELETE localhost:8090/_fake/requests
# ... run the code under test ...
curl -s localhost:8090/_fake/requests | jq '.requests[] | {kind, model, scenario: .headers["X-Fake-Scenario"]}'
```

## Images and versions

| Image | Tags | Platforms |
|---|---|---|
| `ghcr.io/thnhdatle/fakellm` | `v<version>` (for example `v0.1.0`), `latest` | `linux/amd64`, `linux/arm64` |
| `thnhdatle/fakellm:dev` | local build from `make docker` | host architecture |

Released with Triage, same version. Pin a version tag in CI, not `latest`.
