# Roadmap

Last updated: 2026-10-19. Items marked [gfi] are suitable as good first issues.
Nothing here is promised for a date.

## Not in v0.1 (non-goals)

- Team budgets and virtual keys
- Semantic cache
- Admin UI
- Multi-tenant quotas
- Jailbreak detection
- Prompt management
- MCP gateway
- `/v1/responses` and `/v1/completions` endpoints [gfi]
- Azure OpenAI and Bedrock provider adapters
- Persistent decision log and export
- Dry-run route endpoint [gfi]
- Per-key rate limits
- Router training or retraining for custom model pairs

## Planned directions

- Hybrid LLM difficulty strategy: not implemented because there is no public
  checkpoint and training is out of scope for v0.1.
- Release binaries for macOS (darwin/arm64, darwin/amd64): v0.1 publishes Linux archives and
  images only, because the CGO build of `triage` needs a native macOS runner.
- Helm chart for Kubernetes deployment.
- A Go client package for the admin API (v0.1 ships the HTTP API and its OpenAPI document only).
- Team budgets and per-key rate limits, together with a multi-tenant gateway mode (README
  "Comparison": none of this exists in v0.1).
- Semantic cache, admin UI and `/v1/responses` support (listed as missing in the README
  "Comparison").
- Calibration helpers or retraining for custom model pairs: the pretrained checkpoints were
  trained on GPT-4 versus Mixtral outcomes, and with `bert` the cost target was not met for
  the tested Qwen/Gemma pair (README "Benchmarks").
- Multilingual NER (KO and VI): v0.1 NER is English only; no permissively
  licensed KO/VI model was found.

## Future `TODO(roadmap)` items

Items referenced by `TODO(roadmap)` comments in the code are listed here as they
appear.

- (none yet)
