# Operations

Deploy, run, observe and upgrade Triage.

## Commands

```text
triage serve --config triage.yaml          run the router
triage config validate --config triage.yaml  load, validate and check artifacts, print warnings
triage config print [--redact] --config triage.yaml
triage models pull|list                    see models.md
triage calibrate / triage eval             see routing-strategies.md and evaluation.md
triage healthcheck [--url URL] [--config FILE]  exit 0 when /readyz answers 200
triage version
```

Commands read `--config`, else `$TRIAGE_CONFIG`. Exit codes: `0` success, `1` failure, `2`
usage or invalid configuration. Run `triage config validate` before every deploy and reload.

| Variable | Meaning |
|---|---|
| `TRIAGE_CONFIG` | configuration file |
| `TRIAGE_LOG_FORMAT` | `text`; anything else is JSON (default) |
| `TRIAGE_LOG_LEVEL` | `debug`, `info` (default), `warn` or `error` |
| `TRIAGE_ONNX_LIBRARY_PATH` | ONNX Runtime library, see [below](#onnx-runtime-library) |
| `RAYLD_TRIAGE_MODELS_IMAGE`, `TRIAGE_RESEARCH_DIR` | INT8 conversion, see [models.md](models.md) |
| `OTEL_*` | see [Tracing](#tracing) |

Logs go to standard error and never contain prompt text or finding values.

## Deployment

One process, two listeners, no database. Decisions are kept in memory and lost on restart.

### Container image

```sh
docker run --rm -p 8080:8080 -p 127.0.0.1:9091:9091 \
  -v "$PWD/triage.yaml:/etc/triage/config.yaml:ro" \
  -e OPENAI_API_KEY ghcr.io/thnhdatle/triage:v0.1.0
```

The image includes ONNX Runtime. It runs as user 65532 and reads
`/etc/triage/config.yaml`.

- Inside a container, bind `0.0.0.0`. Publish the admin port on a private interface only.
- For `bert` or NER, mount the model cache read-only and set `models.cache_dir`, or run
  `triage models pull` in an init step.
- Compose: `deploy/docker-compose.yml`. Kubernetes: `deploy/kubernetes/`.

### Listeners and authentication

| Listener | Default | Serves |
|---|---|---|
| data plane, `server.listen` | `127.0.0.1:8080` | `POST /v1/chat/completions`, `POST /v1/embeddings`, `GET /v1/models`, `/healthz`, `/readyz`, `/v1/version` |
| admin, `admin.listen` | `127.0.0.1:9091` | `/metrics`, `/healthz`, `/readyz`, `/v1/version`, `GET /v1/routing-decisions[/{id}]`, `GET /v1/admin/config`, `GET /v1/admin/models`, `POST /v1/admin/reload` |

- **The admin listener has no authentication.** Keep it on loopback or a private network.
- The data plane requires `Authorization: Bearer <key>` by default. Keys come from
  `auth.keys_file` (one per line) or `auth.keys_env` (comma-separated), not both.
- To rotate keys, edit the file and reload.
- A non-loopback address with `auth.enabled: false` needs `server.allow_unauthenticated: true`.

### Admin API

```sh
curl -s http://127.0.0.1:9091/v1/routing-decisions?limit=5 | jq .   # recent decisions (limit 1-500, default 50)
curl -s http://127.0.0.1:9091/v1/admin/models | jq .       # routes, targets and their health
curl -s http://127.0.0.1:9091/v1/admin/config | jq .       # active configuration, redacted
```

Decision fields are in `api/schema/decision.v1.json`.

## Health probes

```yaml
health:
  probe: models         # models | completion (default: models)
  interval: 10s         # active probe period (default: 10s)
  failure_threshold: 3  # consecutive failures that mark a target unhealthy (default: 3)
  cooldown: 30s         # how long an unhealthy target is skipped (default: 30s)
```

Real traffic counts too: 429s, 5xx, timeouts and cut streams are failures.
`triage_target_healthy{target}` is 1 or 0.

### `models` probe (default)

`GET {base_url}/models`. Costs no tokens and loads nothing.

### `completion` probe: read this first

`probe: completion` sends a real one-token completion to every target each interval.

**Do not use it with LM Studio.** It forces model loads, can evict the model serving traffic,
and marks targets unhealthy while they load. On cloud providers every probe is billed.

### LM Studio

- **JIT loading.** The first request for an unloaded model waits for the load. Set the
  target's `attempt_timeout` above the load time, or load the model in advance.
- **Auto-evict.** Two local models on one LM Studio swap each other out. Prefer one local
  model per instance.
- **Idle TTL.** An idle model is unloaded (60 minutes by default). The `models` probe does
  not keep it loaded.

## ONNX Runtime library

Needed only for `bert` (or `mf` with `fallback: bert`) and NER. The container image includes
it. Lookup order:

1. `onnx.library_path` (file, or a directory with `libonnxruntime.so` / `libonnxruntime.dylib`);
2. `TRIAGE_ONNX_LIBRARY_PATH` (file or directory);
3. the directory of the `triage` binary;
4. `/opt/homebrew/lib`, then `/usr/local/lib`.

`LD_LIBRARY_PATH` and `/usr/lib` are not searched. Version **1.29 or newer** (tested with
1.31.0). Otherwise startup fails:

```text
onnxrt: no usable ONNX Runtime library (minimum version 1.29); tried: /opt/onnxruntime/libonnxruntime.so (not found); ...
```

```sh
brew install onnxruntime   # or download a release and set TRIAGE_ONNX_LIBRARY_PATH
```

`onnx.library_path` changes only with a restart.

## Hot reload

```sh
kill -HUP $(pidof triage)                                # signal
curl -X POST http://127.0.0.1:9091/v1/admin/reload       # 200, or 422 config_invalid
```

Editing the file also works: Triage checks it every second (works with ConfigMaps).

A rejected file keeps the old configuration active and increments
`triage_config_reload_total{result="error"}`. In-flight requests are not dropped.

Restart-only keys: `server`, `admin.listen`, `auth.enabled`, `onnx.library_path`,
`decisions.buffer_size` and `tracing.capture_content`. Everything else applies on reload.

## Metrics

Prometheus format on `GET /metrics` of the admin listener. No label carries prompt content.

| Metric | Type | Labels |
|---|---|---|
| `triage_http_requests_total` | counter | `method`, `path`, `status` |
| `triage_http_request_duration_seconds` | histogram | `method`, `path` |
| `triage_http_requests_inflight` | gauge | |
| `triage_route_overhead_seconds` | histogram | `route` (request start to dispatch) |
| `triage_upstream_request_duration_seconds` | histogram | `provider`, `model`, `status` |
| `triage_strategy_score` | histogram | `strategy` |
| `triage_scoring_duration_seconds` | histogram | `strategy` |
| `triage_scoring_queue_depth` | gauge | `strategy` |
| `triage_scoring_rejected_total` | counter | `strategy`, `reason` (`timeout`, `rejected`) |
| `triage_tier_decisions_total` | counter | `route`, `tier` |
| `triage_privacy_findings_total` | counter | `route`, `type` |
| `triage_privacy_oversize_total` | counter | `route` |
| `triage_unmask_failures_total` | counter | `route` |
| `triage_mf_external_embeddings_total` | counter | `route` |
| `triage_fallback_attempts_total` | counter | `route`, `reason` |
| `triage_target_inflight` | gauge | `target` |
| `triage_target_healthy` | gauge | `target` |
| `triage_config_reload_total` | counter | `result` (`success`, `error`) |
| `triage_cost_usd_total` | counter | `route`, `target` |
| `triage_build_info` | gauge | `version`, `commit`, `go_version` |

Suggested alerts:

| Symptom | Expression |
|---|---|
| a target is down | `triage_target_healthy == 0` for 5 minutes |
| upstream errors | rate of `triage_http_requests_total{status=~"5.."}` over 5 minutes above your baseline |
| scoring overloaded | `rate(triage_scoring_rejected_total[5m]) > 0` |
| fail-closed privacy | `rate(triage_privacy_findings_total{type="ner_unavailable"}[5m]) > 0`, or `triage_privacy_oversize_total` rising |
| model altered placeholders | `rate(triage_unmask_failures_total[15m]) > 0` |
| bad configuration pushed | `increase(triage_config_reload_total{result="error"}[10m]) > 0` |
| router overhead | `histogram_quantile(0.99, rate(triage_route_overhead_seconds_bucket[5m]))` above your budget |

## Tracing

Off unless an OTLP endpoint is set. Standard `OTEL_*` variables apply.

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 triage serve --config triage.yaml   # http/protobuf by default
```

- `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` switches to gRPC.
- `OTEL_SDK_DISABLED=true` turns tracing off.
- Incoming W3C trace context is continued; provider requests carry `traceparent`.

No prompt text is on spans by default. **With `tracing.capture_content: true`, local
attempts record the original, unmasked text.** Changing it needs a restart.

## Readiness, liveness and shutdown

- `GET /healthz`: 200 while running.
- `GET /readyz`: 200 once the configuration and models are loaded. Upstream outages do not
  make it fail.
- On `SIGTERM` or `SIGINT`, Triage drains in-flight requests for up to 30 s. In Kubernetes,
  set `terminationGracePeriodSeconds` above 30 and a short `preStop` sleep.
- `triage healthcheck` checks `/readyz` without `curl`.

## Upgrade and backup

- **Upgrade.** Run `triage config validate` with the new binary first. Then replace and
  restart. If `triage models list` shows mismatches, run `triage models pull`. If a release
  changes models, re-check thresholds with `triage calibrate`.
- **Backup.** Back up the configuration and the API key file. Nothing else.
- **Secrets.** Provider keys come from `api_key_env` or `api_key_file`, never the
  configuration. `triage config print --redact` and `GET /v1/admin/config` hide
  `extra_headers` values and credentials in URLs.
