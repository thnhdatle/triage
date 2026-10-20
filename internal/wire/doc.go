// SPDX-License-Identifier: Apache-2.0

// Package wire is the composition root of the triage binary: it turns the
// configuration into adapters and use cases and runs the subcommands (serve,
// models, calibrate, eval, config, healthcheck, version). cmd/triage only
// owns the process signals (wiring moves out of main.go once
// it outgrows it; it cannot live under internal/app, whose imports depguard
// restricts to domain and platform).
//
// serve builds one generation per configuration: provider clients (one
// connection pool per provider, per-target extra_headers, traceparent
// injection), strategies (random, length, bert on the process-wide ONNX
// Runtime, mf behind the usage embedder and the LRU cache), the regex and
// optional NER detectors, the dispatcher, the route pipeline, health checks.
// A reload builds the next generation before swapping it in (missing
// artifacts reject the reload); resources whose inputs did not change are
// shared, and a retired generation's resources close when its last request
// ends.
//
//	code := (&wire.CLI{}).Run(ctx, []string{"serve", "--config", "triage.yaml"})
package wire
