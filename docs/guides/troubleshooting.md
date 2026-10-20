# Troubleshooting

Find out why Triage does not start, fails a request or routes unexpectedly.

Start with the decision. Take the `X-Triage-Decision` header of the response:

```sh
curl -s http://127.0.0.1:9091/v1/routing-decisions/<decision id> | jq .   # privacy, score, tier, reasons, attempts
```

Error codes are in [errors.md](errors.md). Logs go to standard error, without prompt text.
Search them by `X-Request-Id`. Set `TRIAGE_LOG_LEVEL=debug` for more detail.

## The process does not start

| Symptom | Fix |
|---|---|
| `triage config validate` prints problems and exits 2 | Fix every listed key; unknown keys are errors. See [configuration.md](configuration.md). |
| `non-loopback address "0.0.0.0:8080" without auth requires server.allow_unauthenticated: true` | Enable auth (`auth.keys_file` or `auth.keys_env`), or set `server.allow_unauthenticated: true` knowingly. |
| `keys_file and keys_env are mutually exclusive` | Set only one of them. |
| `onnxrt: no usable ONNX Runtime library (minimum version 1.29); tried: ...` | Install ONNX Runtime 1.29 or newer; set `TRIAGE_ONNX_LIBRARY_PATH` or `onnx.library_path`. See [operations.md](operations.md#onnx-runtime-library). |
| An error ending in `run "triage models pull bert"` (or `mf`, `ner`) | Run `triage models pull <name>`, then `triage models list`. See [models.md](models.md). |
| `bert` is rejected on a `noonnx` build | Use a normal build, or `difficulty.missing_artifact: fallback`. See [models.md](models.md#builds-without-onnx-runtime). |
| A route using `mf` is rejected | Set `difficulty.mf.allow_external_embedding: true` and its API key. See [privacy.md](privacy.md). |

`GET /readyz` answers 200 once the configuration and models are loaded. Use
`triage healthcheck` where `curl` is missing.

## Requests fail

| Code | What to check |
|---|---|
| `invalid_api_key` (401) | `Authorization: Bearer <key>`; key in `auth.keys_file` or `auth.keys_env`. |
| `model_not_found` (404) | `model` must be a route name. See `GET /v1/models`. |
| `privacy_mode_not_allowed` (400) | `X-Triage-Privacy` stricter than `max_client_mode`, or not `off`, `mask`, `local_only`. |
| `privacy_no_local_target` (503) | No healthy local target fits. Check `triage_target_healthy` and `reasons`. |
| `no_eligible_target` (503) | Check `GET /v1/admin/models` for health; `mask_leak` in the decision. |
| `upstream_bad_request` (400) | Provider rejected it, e.g. a wrong provider key. Not retried. |
| `upstream_timeout` (504), `upstream_unavailable` (502), `rate_limit_exceeded` (429) | Retries used up. See the attempts and [fallback.md](fallback.md). |
| `request_too_large` (413) | Body above `api.max_body_bytes` (default 8 MiB). |
| `privacy_scan_limit` (413) | Text above `privacy.max_scan_bytes`; oversize policy rejects. |

## Routing is not what you expect

- Read `reasons` in the decision. A rule match skips the strategy. Set
  `api.expose_decision_detail: true` to get them in `X-Triage-Route-Reason`.
- **Everything goes to strong.** Scoring may be failing (`difficulty.on_error: strong` is the
  default). Check `triage_scoring_rejected_total{reason}`. Calibrate the threshold for your
  model pair with `triage calibrate`.
- **Personal data stays local.** That is the privacy stage. See `privacy` in the decision.
- **`mf` is not used.** Findings or a failed scan switch to `difficulty.mf.fallback`
  (`strategy:mf->bert(privacy)`).

## Privacy

- **A value was not masked.** Only detectable formats are found. Names need NER
  (`privacy.ner.enabled`). See [privacy.md](privacy.md).
- **A placeholder such as `[[EMAIL_1]]` reached the client.** The model altered it. Look for
  `X-Triage-Unmask: partial`, `: triage-unmask=partial` on streams, or
  `triage_unmask_failures_total`.
- **`privacy_no_local_target` after enabling NER.** `ner_unavailable` keeps requests local.
  Alert on `triage_privacy_findings_total{type="ner_unavailable"}`.

## Streams

- **Ends without `data: [DONE]`.** The stream failed and fallback could not fix it. See
  [fallback.md](fallback.md).
- **Overlap or restart in the answer.** A continuation happened. The decision lists every
  attempt; follow-ups have `continuation: true`.

## Slow first requests with LM Studio

The first request waits for LM Studio to load the model. Set the target's `attempt_timeout`
above the load time, or load the model in advance. Do not use `health.probe: completion`
with LM Studio. See [operations.md](operations.md#lm-studio).

## Reload

`422 config_invalid`, or a rising `triage_config_reload_total{result="error"}`: the new file
was rejected and the old one is still active. Run `triage config validate` on it.

Changes to `server`, `admin.listen`, `auth.enabled`, `onnx.library_path`,
`decisions.buffer_size` and `tracing.capture_content` need a restart.

## Missing decisions

`404 decision_not_found`: the decision left the buffer (`decisions.buffer_size`) or the
process restarted.

The admin listener has no authentication. It is on loopback by default. From a container,
publish it explicitly.
