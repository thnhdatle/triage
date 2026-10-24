# Contributing to Triage

Set up, run the checks and send a pull request. Read the [Code of Conduct](CODE_OF_CONDUCT.md)
first.

## Prerequisites

- Go (version in `go.mod`), Python 3.12 with uv, Node.js 20 or later, Docker (OrbStack,
  Colima or Docker Desktop), make, git, curl, jq, yq.
- golangci-lint v2.14, hadolint, shellcheck, gitleaks, kubeconform, govulncheck, trivy;
  goreleaser and syft for `make release-dry`.
- ONNX Runtime 1.31 shared library, found through `TRIAGE_ONNX_LIBRARY_PATH` or the default
  library search path.

On macOS with Homebrew:

```bash
brew install go python@3.12 uv jq yq node onnxruntime                                       # runtimes
brew install golangci-lint goreleaser gitleaks trivy hadolint shellcheck kubeconform syft   # linters, scanners
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0                                         # vulnerability check
```

Other Go tools are pinned in `go.mod` and run with `go tool <name>`.

## Getting started

```bash
make bootstrap   # check tools, download modules, set up git hooks, fetch pinned test artifacts
make ci          # lint, test, test-integration, coverage, examples, security
```

## Make targets

| Target | Purpose |
|---|---|
| `help` | list targets |
| `bootstrap` | tools, modules, hooks, test artifacts |
| `generate` | code generation (none in v0.1) |
| `fmt` | format all code |
| `lint` | golangci-lint, ruff, mypy, hadolint, shellcheck, gitleaks, kubeconform |
| `test` | unit tests with `-race` |
| `test-integration` | integration tests (Testcontainers) |
| `coverage` | coverage with thresholds |
| `eval` | small eval runs, about 10 minutes (live part needs LM Studio) |
| `fuzz` | each fuzz target for `FUZZTIME` |
| `examples` | run examples, check output |
| `quickstart-test` | README quickstart from a clean clone |
| `security` | govulncheck, go-licenses, trivy |
| `build` | binaries into `./bin` |
| `models` | pull and derive checkpoints (FP32, INT8) |
| `docker` | local images, host architecture |
| `up` / `down` | compose stack up / down |
| `ci` | lint, test, test-integration, coverage, examples, security |
| `release-check` | pre-release checks, before tagging |
| `release-dry` | host-platform release build, no publish |

## Test levels

```bash
make test               # unit
make test-integration   # integration, needs Docker
make fuzz               # fuzz
make eval               # small eval runs (live part needs LM Studio)
```

## Commit format

One line only, no body or trailers:

```text
<type>(<scope>): <summary>
```

- Types: `feat, fix, perf, refactor, test, docs, build, ci, chore`.
- Scope: the area you change, for example `router`, `pii`, `strategy`, `api`, `deps`.
- Summary: starts lowercase, no final period.
- Whole line: at most 72 characters.

Example: `feat(strategy): add length difficulty strategy`. The `commit-msg` hook from
`make bootstrap` enforces this.

## Adding a dependency

- Maintained: activity in the last 12 months.
- License: Apache-2.0, MIT, MIT-CMU, BSD-2-Clause, BSD-3-Clause, ISC, MPL-2.0, 0BSD, Zlib,
  PSF-2.0, Unlicense or CC0-1.0.
- No transitive GPL/AGPL.

`make security` checks the licenses. Record the checklist in the PR. Never commit model weights
or datasets.

## Pull request checklist

- [ ] Tests added or updated, `make ci` passes
- [ ] Docs updated
- [ ] `CHANGELOG.md` updated under `[Unreleased]`
- [ ] Commit messages follow the format above

## Questions

Open a GitHub Discussion or an issue. Security reports: [SECURITY.md](SECURITY.md).
