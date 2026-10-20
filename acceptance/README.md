# Acceptance suite

Run the end-to-end tests behind Triage's guarantees and read the report. The numbers in the
README and in [`bench/RESULTS.md`](../bench/RESULTS.md) come from these tests.

## Running

```sh
make acceptance                      # every criterion, reference-machine thresholds
make acceptance CI=1                 # CI thresholds
make acceptance ONLY=TRG-01,TRG-04   # a subset
```

It builds and starts the stack, runs each criterion and writes `acceptance/report.md`. It exits
non-zero if any criterion fails.

| Setting | Effect |
|---|---|
| `TRIAGE_ACCEPTANCE_BUILD` | shell command that replaces `make build` and `make docker` |
| `--version` | version in the report heading (`make acceptance` passes `RELEASE`) |

Each criterion needs exactly one test, named as in `criteria.yaml`, and no skip construct. The
report row `criteria` checks this.

A full run takes about 45 minutes on the reference machine, plus about 35 minutes for TRG-03.

### Requirements

| Requirement | Needed by | Notes |
|---|---|---|
| Docker (OrbStack, Colima or Docker Desktop) | most criteria | 8 CPUs; at most 10 GB memory next to LM Studio |
| Python 3.12 and `uv` | TRG-05, TRG-11 | |
| Node.js 20 or later | TRG-05 | |
| Network access | TRG-10, TRG-12 | until cached in `~/.cache/rayld/triage` (about 1.1 GB) |
| ONNX Runtime 1.31 | TRG-03, TRG-11, TRG-13 | or set `TRIAGE_ONNX_LIBRARY_PATH` |
| Apple-silicon Mac (darwin/arm64) | TRG-02, TRG-03, TRG-08, TRG-13 | reference: MacBook Pro M3 Pro, 36 GB |
| LM Studio | TRG-03 | see below |

### LM Studio (TRG-03)

TRG-03 needs a weak and a strong model in LM Studio and at least 18 GB of free host memory.
Otherwise it is reported as Skipped.

| Variable | Default |
|---|---|
| `TRIAGE_LMSTUDIO_BASE_URL` | `http://localhost:1234/v1` |
| `TRIAGE_TRG03_WEAK_MODEL` | `qwen/qwen3-vl-4b` |
| `TRIAGE_TRG03_STRONG_MODEL` | `gemma-4-26b-a4b-it-qat-mlx` |

`OPENAI_API_KEY` is optional. Without it, the `mf` strategy is reported as not measured.

## Criteria

Thresholds are in [`criteria.yaml`](criteria.yaml): `pass.mac` for the reference machine,
`pass.ci` for CI (`skip` where a criterion cannot run on CI). Tests are named
`TestAcceptance_TRGnn_…` or `BenchmarkAcceptance_TRGnn_…` in `trgNN_test.go`.

| ID | Kind | What it checks |
|---|---|---|
| TRG-01 | functional | No unmasked PII reaches a cloud target |
| TRG-02 | performance | p99 overhead below 5 ms at 500 RPS |
| TRG-03 | evaluation | Live `bert` cost and quality, LM Studio pair |
| TRG-04 | functional | Fallback completes at least 99% of requests |
| TRG-05 | functional | OpenAI Python and Node SDKs work unmodified |
| TRG-06 | functional | Mask round trip restores every placeholder |
| TRG-07 | functional | Hot reload: no router 5xx, invalid config never applied |
| TRG-08 | performance | Sustained non-streaming throughput |
| TRG-09 | functional | Every response has a decision; spans carry it |
| TRG-10 | evaluation | Offline APGR beats random (RouteLLM labels) |
| TRG-11 | functional | Go matches the Python and PyTorch references |
| TRG-12 | release | README quickstart works |
| TRG-13 | performance | `bert` INT8 scoring latency |

Performance thresholds are for the reference machine. On another host the criterion is
Blocked. Run with `CI=1` to use the CI thresholds.

## The report

[`report.md`](report.md) has one row per criterion. Local logs: `acceptance/logs/<ID>.txt` and
`<ID>.json` (not committed).

| Result | Meaning |
|---|---|
| **PASS** | test passed, every value meets its threshold |
| **FAIL** | a value misses its threshold; listed under "Not met" with the reason from [`not_met.yaml`](not_met.yaml) |
| **Skipped (environment)** | no thresholds here (`ci: skip`), or optional service (network, LM Studio) missing |
| **Blocked (environment)** | required tool or host missing (Docker, Python, Node, reference Mac) |

The runner, not the test, decides Skipped and Blocked.
