# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-10-21

First public release.

### Added

- OpenAI-compatible API: `POST /v1/chat/completions` (streaming and non-streaming, tools),
  `POST /v1/embeddings` and `GET /v1/models`, with bearer-key authentication, a separate admin
  listener, `/healthz`, `/readyz` and `/v1/version`.
- Providers: OpenAI-compatible backends (OpenAI, vLLM, Ollama, LM Studio) and Anthropic, over
  raw HTTP with error normalisation and Anthropic translation.
- Model pool with active and passive health checks and tier-based target selection.
- Rule-based routing.
- Privacy stage with modes `off`, `mask` and `local_only`: regex and checksum detectors for nine
  types and nineteen formats, optional English NER, per-attempt masking by target
  locality with `[[TYPE_n]]` placeholders, and unmasking of responses, streams and tool-call
  arguments.
- Difficulty strategies `random`, `length`, `bert` (RouteLLM BERT router, INT8 block-wise
  quantisation, ONNX Runtime in process) and optional `mf` with an explicit
  data-egress opt-in. Pure-Go tokenizer with a parity policy.
- Dispatch with retries, fallback chains and mid-stream continuation.
- Routing decisions with an explain API (`GET /v1/routing-decisions`), cost accounting,
  Prometheus metrics and OpenTelemetry traces.
- Hot configuration reload (SIGHUP, admin endpoint and file polling) and `triage config
  validate` and `print`.
- `triage models pull` and `list`: pinned, SHA-256-verified model artifacts.
- `triage calibrate` and `triage eval`: threshold calibration and an evaluation harness with
  the APGR metric.
- `fakellm`, a scriptable fake OpenAI-compatible provider for tests, the quickstart and
  benchmarks, released as `ghcr.io/thnhdatle/fakellm` ([reference](docs/reference/fakellm.md)).
- Release archives for Linux amd64 and arm64 with `checksums.txt` and SPDX SBOMs, and the
  multi-arch images `ghcr.io/thnhdatle/triage` (ONNX Runtime included) and
  `ghcr.io/thnhdatle/fakellm`; `deploy/docker-compose.yml` quickstart and Kubernetes manifests.
- Guides for configuration, routing strategies, privacy, fallback, models, evaluation,
  operations, troubleshooting and error codes; runnable examples in `examples/`.
- Benchmarks (`bench/RESULTS.md`): router overhead p99 1.73 ms at 500 RPS and 7,999 RPS
  sustained with `length` and `mask`, against a zero-latency fake upstream on one MacBook Pro
  M3 Pro.

### Known limitations

- With the pretrained `bert` checkpoint, the cost target (at most 75% of the all-strong cost
  while keeping 96% of its accuracy) is not met for the tested local pair (`qwen3-vl-4b` weak,
  `gemma-4-26b` strong): 92.3% of the cost. The result depends on the weak/strong model pair;
  calibrate for your own pair. See the README "Benchmarks" section.
- `mf` is not evaluated for routing quality (it needs OpenAI embeddings).
- Release binaries are built for Linux only; on macOS build from source or use the image.
- Privacy protection covers detectable formats only; NER is English only and off by default.
- Deferred scope is listed in `ROADMAP.md`.

[Unreleased]: https://github.com/thnhdatle/triage/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/thnhdatle/triage/releases/tag/v0.1.0
