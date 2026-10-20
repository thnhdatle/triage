# Benchmark results

Written by `make bench` (scripts/bench.sh); each scenario runs 3 times.

<!-- scenario:TRG-02 -->
## Router overhead (TRG-02; length + mask, 500 RPS for 60 s) — 2026-10-19
Machine: MacBook Pro M3 Pro 36 GB, macOS 26.1, Docker VM 8 CPU / 10 GB, Go 1.27.2, Triage v0.1.0 pre-release build

| Metric | Median | Min | Max | Threshold | Pass |
|---|---|---|---|---|---|
| p99 paired overhead (ms) | 1.731 | 1.215 | 1.950 | < 5 | yes (3/3 runs) |
| p50 paired overhead (ms) | 0.344 | 0.343 | 0.356 | - | - |
| p99 overhead excluding scoring (ms, route without strategy) | 1.146 | 1.064 | 1.831 | - | - |
| p50 overhead excluding scoring (ms) | 0.326 | 0.322 | 0.332 | - | - |
| p99 latency via Triage (ms) | 4.377 | 3.975 | 4.473 | - | - |
| p99 latency direct to fakellm (ms) | 3.776 | 3.417 | 3.876 | - | - |
| failed requests | 0.000 | 0.000 | 0.000 | = 0 | yes (3/3 runs) |

Paired design: each scheduled request goes at the same instant directly to fakellm and through
Triage; overhead = p99 of (Triage − direct).
<!-- /scenario:TRG-02 -->

<!-- scenario:TRG-08 -->
## Throughput (TRG-08; non-streaming, length + mask, open-model steps) — 2026-10-19
Machine: MacBook Pro M3 Pro 36 GB, macOS 26.1, Docker VM 8 CPU / 10 GB, Go 1.27.2, Triage v0.1.0 pre-release build

| Metric | Median | Min | Max | Threshold | Pass |
|---|---|---|---|---|---|
| sustained throughput (RPS) | 7999.000 | 7999.000 | 8000.000 | >= 3000 | yes (3/3 runs) |
| p99 at sustained rate (ms) | 6.153 | 5.893 | 11.030 | < 20 | yes (3/3 runs) |
| bert (INT8) saturation through the router (RPS, reported) | 9.993 | 9.993 | 9.993 | - | - |

Sustained = highest step of the ladder with 0 errors, ≥ 98% of the offered rate completed and p99
under the threshold. Per-step numbers: `acceptance/logs/TRG-08.json` (last run).
<!-- /scenario:TRG-08 -->

<!-- scenario:TRG-13 -->
## bert scoring latency (TRG-13; INT8, in-process Score) — 2026-10-19
Machine: MacBook Pro M3 Pro 36 GB, macOS 26.1, Docker VM 8 CPU / 10 GB, Go 1.27.2, Triage v0.1.0 pre-release build

| Metric | Median | Min | Max | Threshold | Pass |
|---|---|---|---|---|---|
| p99 (ms, median of the benchmark's runs) | 164.000 | 164.000 | 164.000 | < 250 | yes (1/1 runs) |
| p50 (ms) | 74.350 | 74.350 | 74.350 | - | - |
| p50 at 256 tokens (ms) | 62.610 | 62.610 | 62.610 | - | - |
| saturation throughput (req/s) | 19.730 | 19.730 | 19.730 | - | - |
| saturation at 256 tokens (req/s) | 20.770 | 20.770 | 20.770 | - | - |

The benchmark performs its three runs internally and reports their medians; per-run values and
min–max: `acceptance/logs/TRG-13.json`.
<!-- /scenario:TRG-13 -->

<!-- scenario:micro -->
## Package micro-benchmarks — 2026-10-19
Machine: MacBook Pro M3 Pro 36 GB, macOS 26.1, Docker VM 8 CPU / 10 GB, Go 1.27.2, Triage v0.1.0 pre-release build

```text
goos: darwin
goarch: arm64
pkg: github.com/thnhdatle/triage/internal/adapter/piiregex
cpu: Apple M3 Pro
                         │      micro-20261019-171427.txt      │
                         │               sec/op                │
Detector_Scan2KB-11                               145.6µ ± ∞ ¹
Detector_ScanProse2KB-11                          24.35µ ± ∞ ¹
geomean                                           59.55µ
¹ need >= 6 samples for confidence interval at level 0.95

                         │      micro-20261019-171427.txt      │
                         │                 B/s                 │
Detector_Scan2KB-11                              16.21Mi ± ∞ ¹
Detector_ScanProse2KB-11                         80.21Mi ± ∞ ¹
geomean                                          36.06Mi
¹ need >= 6 samples for confidence interval at level 0.95

                         │      micro-20261019-171427.txt      │
                         │                B/op                 │
Detector_Scan2KB-11                              19.49Ki ± ∞ ¹
Detector_ScanProse2KB-11                         1.195Ki ± ∞ ¹
geomean                                          4.827Ki
¹ need >= 6 samples for confidence interval at level 0.95

                         │      micro-20261019-171427.txt      │
                         │              allocs/op              │
Detector_Scan2KB-11                                301.0 ± ∞ ¹
Detector_ScanProse2KB-11                           39.00 ± ∞ ¹
geomean                                            108.3
¹ need >= 6 samples for confidence interval at level 0.95

pkg: github.com/thnhdatle/triage/internal/adapter/strategy/bert
              │      micro-20261019-171427.txt      │
              │               sec/op                │
Score/fp32-11                          55.91µ ± ∞ ¹
Score/int8-11                          57.99µ ± ∞ ¹
geomean                                56.94µ
¹ need >= 6 samples for confidence interval at level 0.95

              │      micro-20261019-171427.txt      │
              │                B/op                 │
Score/fp32-11                         6.719Ki ± ∞ ¹
Score/int8-11                         6.719Ki ± ∞ ¹
geomean                               6.719Ki
¹ need >= 6 samples for confidence interval at level 0.95

              │      micro-20261019-171427.txt      │
              │              allocs/op              │
Score/fp32-11                           73.00 ± ∞ ¹
Score/int8-11                           73.00 ± ∞ ¹
geomean                                 73.00
¹ need >= 6 samples for confidence interval at level 0.95

pkg: github.com/thnhdatle/triage/internal/adapter/strategy/mf
         │      micro-20261019-171427.txt      │
         │               sec/op                │
Score-11                          1.860µ ± ∞ ¹
¹ need >= 6 samples for confidence interval at level 0.95

         │      micro-20261019-171427.txt      │
         │                B/op                 │
Score-11                           72.00 ± ∞ ¹
¹ need >= 6 samples for confidence interval at level 0.95

         │      micro-20261019-171427.txt      │
         │              allocs/op              │
Score-11                           3.000 ± ∞ ¹
¹ need >= 6 samples for confidence interval at level 0.95

pkg: github.com/thnhdatle/triage/internal/platform/hftok
                               │      micro-20261019-171427.txt      │
                               │               sec/op                │
Encode_512Tokens/bert/en-11                             138.9µ ± ∞ ¹
Encode_512Tokens/bert/ko-11                             130.0µ ± ∞ ¹
Encode_512Tokens/bert/mixed-11                          101.6µ ± ∞ ¹
Encode_512Tokens/ner/en-11                              38.71µ ± ∞ ¹
Encode_512Tokens/ner/ko-11                              52.27µ ± ∞ ¹
Encode_512Tokens/ner/mixed-11                           41.76µ ± ∞ ¹
geomean                                                 73.29µ
¹ need >= 6 samples for confidence interval at level 0.95

                               │      micro-20261019-171427.txt      │
                               │                 B/s                 │
Encode_512Tokens/bert/en-11                            13.30Mi ± ∞ ¹
Encode_512Tokens/bert/ko-11                            22.07Mi ± ∞ ¹
Encode_512Tokens/bert/mixed-11                         21.19Mi ± ∞ ¹
Encode_512Tokens/ner/en-11                             47.74Mi ± ∞ ¹
Encode_512Tokens/ner/ko-11                             54.88Mi ± ∞ ¹
Encode_512Tokens/ner/mixed-11                          51.54Mi ± ∞ ¹
geomean                                                30.72Mi
¹ need >= 6 samples for confidence interval at level 0.95

                               │      micro-20261019-171427.txt      │
                               │                B/op                 │
Encode_512Tokens/bert/en-11                            141.9Ki ± ∞ ¹
Encode_512Tokens/bert/ko-11                            170.6Ki ± ∞ ¹
Encode_512Tokens/bert/mixed-11                         130.8Ki ± ∞ ¹
Encode_512Tokens/ner/en-11                             28.36Ki ± ∞ ¹
Encode_512Tokens/ner/ko-11                             33.36Ki ± ∞ ¹
Encode_512Tokens/ner/mixed-11                          20.61Ki ± ∞ ¹
geomean                                                62.87Ki
¹ need >= 6 samples for confidence interval at level 0.95

                               │      micro-20261019-171427.txt      │
                               │              allocs/op              │
Encode_512Tokens/bert/en-11                              759.0 ± ∞ ¹
Encode_512Tokens/bert/ko-11                              575.0 ± ∞ ¹
Encode_512Tokens/bert/mixed-11                           677.0 ± ∞ ¹
Encode_512Tokens/ner/en-11                               12.00 ± ∞ ¹
Encode_512Tokens/ner/ko-11                               12.00 ± ∞ ¹
Encode_512Tokens/ner/mixed-11                            11.00 ± ∞ ¹
geomean                                                  88.11
¹ need >= 6 samples for confidence interval at level 0.95
```
<!-- /scenario:micro -->
