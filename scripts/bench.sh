#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# make bench: runs every performance scenario 3 times and
# rewrites its section of bench/RESULTS.md (median and min-max per metric,
# threshold from acceptance/criteria.yaml, pass when every run meets it), then
# the package micro-benchmarks. Triage runs natively in the benchmark process;
# the benchmarks start their own fakellm container (--record=false) from
# thnhdatle/fakellm:dev, so no compose stack is needed.
#
#   BENCH_ONLY="TRG-02 TRG-08"  run a subset (TRG-02 TRG-08 TRG-13 micro)
#   BENCH_COUNT=3               runs per scenario
#   CI=1                        CI thresholds
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
export TRIAGE_ACCEPTANCE_VERSION="${RELEASE:-dev}"

only="${BENCH_ONLY:-TRG-02 TRG-08 TRG-13 micro}"
count="${BENCH_COUNT:-3}"
env_name=mac
[ "${CI:-}" = "1" ] && env_name=ci
out_dir=out/bench # raw benchmark output, gitignored
results=bench/RESULTS.md
mkdir -p "$out_dir"

want() { [[ " $only " == *" $1 "* ]]; }

# --- machine line -------------------------------------------------------------
machine_line() {
	local model mem os docker_cpu docker_mem gover
	model="$(sysctl -n machdep.cpu.brand_string 2>/dev/null | sed 's/^Apple //' || uname -m)"
	mem="$(($(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1073741824))"
	os="$(sw_vers -productVersion 2>/dev/null || uname -r)"
	docker_cpu="$(docker info --format '{{.NCPU}}' 2>/dev/null || echo '?')"
	docker_mem="$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)"
	docker_mem="$(((docker_mem + 536870912) / 1073741824))"
	gover="$(go env GOVERSION | sed 's/^go//')"
	echo "Machine: $model $mem GB, macOS $os, Docker VM $docker_cpu CPU / $docker_mem GB, Go $gover, Triage ${RELEASE:-dev}"
}

# threshold <id> <key>: the value of key in pass.<env> of criterion id, empty when absent.
threshold() {
	yq -r ".criteria[] | select(.id == \"$1\") | .pass.$env_name.$2 // \"\"" acceptance/criteria.yaml 2>/dev/null || true
}

# --- fakellm image --------------------------------------------------------------
ensure_fakellm() {
	local image="${RAYLD_FAKELLM_IMAGE:-thnhdatle/fakellm:dev}"
	if ! docker image inspect "$image" >/dev/null 2>&1; then
		echo "bench: building $image"
		docker build -f Dockerfile.fakellm -t "$image" . >/dev/null
	fi
}

# run_bench <pattern> <raw file> <count>: go test benchmarks of the acceptance package.
run_bench() {
	go test -tags acceptance -count="$3" -timeout 3h -run '^$' -bench "$1" -benchtime 1x ./acceptance/ 2>&1 |
		tee "$2" | grep -E '^(Benchmark|PASS|FAIL|ok|---|\s+trg)' || true
	if grep -qE '^(FAIL|--- FAIL)' "$2"; then
		echo "bench: $1 reported a failure (see $2)" >&2
		return 1
	fi
}

# stats <raw file> <benchmark name> <unit>: "median min max n" over the runs.
stats() {
	awk -v name="$2" -v unit="$3" '
		$1 ~ "^"name"(-[0-9]+)?$" { for (i = 3; i < NF; i += 2) if ($(i+1) == unit) v[n++] = $i }
		END {
			if (n == 0) { print "- - - 0"; exit }
			for (i = 0; i < n; i++) for (j = i + 1; j < n; j++) if (v[j] < v[i]) { t = v[i]; v[i] = v[j]; v[j] = t }
			med = (n % 2) ? v[int(n/2)] : (v[n/2-1] + v[n/2]) / 2
			printf "%.3f %.3f %.3f %d\n", med, v[0], v[n-1], n
		}' "$1"
}

# row <raw> <bench> <unit> <label> <op> <threshold>: one table row; op is "<", ">=", "=" or "".
row() {
	local raw="$1" bench="$2" unit="$3" label="$4" op="$5" thr="$6" med lo hi n pass="-" thr_txt="-"
	read -r med lo hi n < <(stats "$raw" "$bench" "$unit")
	if [ -n "$op" ] && [ -n "$thr" ] && [ "$n" -gt 0 ]; then
		thr_txt="$op $thr"
		# Pass only when every run meets the threshold.
		if awk -v lo="$lo" -v hi="$hi" -v op="$op" -v t="$thr" 'BEGIN {
			ok = (op == "<") ? (hi < t) : (op == ">=") ? (lo >= t) : (lo == t && hi == t); exit !ok }'; then
			pass="yes ($n/$n runs)"
		else
			pass="**no**"
			failed=1
		fi
	fi
	echo "| $label | $med | $lo | $hi | $thr_txt | $pass |"
}

# replace_section <id> <file>: put file's content between the markers of id in RESULTS.md.
replace_section() {
	local id="$1" body="$2" tmp
	tmp="$(mktemp)"
	[ -f "$results" ] || printf '# Benchmark results\n\nWritten by %s (scripts/bench.sh); each scenario runs %s times.\n' \
		"\`make bench\`" "$count" >"$results"
	awk -v id="$id" -v body="$body" '
		BEGIN { start = "<!-- scenario:" id " -->"; stop = "<!-- /scenario:" id " -->" }
		$0 == start { skip = 1; while ((getline l < body) > 0) print l; done = 1; next }
		$0 == stop { skip = 0; next }
		!skip { print }
		END { if (!done) { print ""; while ((getline l < body) > 0) print l } }' "$results" >"$tmp"
	mv "$tmp" "$results"
}

section_head() { # section_head <title>
	echo "<!-- scenario:$1 -->"
	echo "## $2 — $(date +%Y-%m-%d)"
	machine_line
	echo
	echo "| Metric | Median | Min | Max | Threshold | Pass |"
	echo "|---|---|---|---|---|---|"
}

failed=0
date_tag="$(date +%Y%m%d-%H%M%S)"

if want TRG-02; then
	ensure_fakellm
	raw="$out_dir/trg02-$date_tag.txt"
	echo "bench: TRG-02 x$count (2 x 65 s per run)"
	run_bench '^Benchmark(Acceptance_TRG02_Overhead|Perf_TRG02_OverheadNoScoring)$' "$raw" "$count" || failed=1
	sec="$(mktemp)"
	{
		section_head TRG-02 "Router overhead (TRG-02; length + mask, $(threshold TRG-02 rps) RPS for $(threshold TRG-02 duration_s) s)"
		row "$raw" BenchmarkAcceptance_TRG02_Overhead p99-overhead-ms "p99 paired overhead (ms)" "<" "$(threshold TRG-02 p99_overhead_ms_max)"
		row "$raw" BenchmarkAcceptance_TRG02_Overhead p50-overhead-ms "p50 paired overhead (ms)" "" ""
		row "$raw" BenchmarkPerf_TRG02_OverheadNoScoring p99-overhead-ms "p99 overhead excluding scoring (ms, route without strategy)" "" ""
		row "$raw" BenchmarkPerf_TRG02_OverheadNoScoring p50-overhead-ms "p50 overhead excluding scoring (ms)" "" ""
		row "$raw" BenchmarkAcceptance_TRG02_Overhead p99-triage-ms "p99 latency via Triage (ms)" "" ""
		row "$raw" BenchmarkAcceptance_TRG02_Overhead p99-direct-ms "p99 latency direct to fakellm (ms)" "" ""
		row "$raw" BenchmarkAcceptance_TRG02_Overhead errors "failed requests" "=" 0
		echo
		echo "Paired design: each scheduled request goes at the same instant directly to fakellm and through"
		echo "Triage; overhead = p99 of (Triage − direct)."
		echo "<!-- /scenario:TRG-02 -->"
	} >"$sec"
	replace_section TRG-02 "$sec"
	rm -f "$sec"
fi

if want TRG-08; then
	ensure_fakellm
	raw="$out_dir/trg08-$date_tag.txt"
	echo "bench: TRG-08 x$count (~3 min per run)"
	run_bench '^BenchmarkAcceptance_TRG08_Throughput$' "$raw" "$count" || failed=1
	sec="$(mktemp)"
	p99max="$(threshold TRG-08 p99_ms_max)"
	{
		section_head TRG-08 "Throughput (TRG-08; non-streaming, length + mask, open-model steps)"
		row "$raw" BenchmarkAcceptance_TRG08_Throughput rps "sustained throughput (RPS)" ">=" "$(threshold TRG-08 rps_min)"
		if [ -n "$p99max" ]; then
			row "$raw" BenchmarkAcceptance_TRG08_Throughput p99-ms "p99 at sustained rate (ms)" "<" "$p99max"
		else
			row "$raw" BenchmarkAcceptance_TRG08_Throughput p99-ms "p99 at sustained rate (ms)" "" ""
		fi
		row "$raw" BenchmarkAcceptance_TRG08_Throughput bert-rps "bert (INT8) saturation through the router (RPS, reported)" "" ""
		echo
		echo "Sustained = highest step of the ladder with 0 errors, ≥ 98% of the offered rate completed and p99"
		echo "under the threshold. Per-step numbers: \`acceptance/logs/TRG-08.json\` (last run)."
		echo "<!-- /scenario:TRG-08 -->"
	} >"$sec"
	replace_section TRG-08 "$sec"
	rm -f "$sec"
fi

if want TRG-13 && ! grep -q 'notImplemented(b, "TRG-13")' acceptance/trg13_test.go; then
	raw="$out_dir/trg13-$date_tag.txt"
	echo "bench: TRG-13 x1 (the benchmark runs its 3 open-model runs itself)"
	run_bench '^BenchmarkAcceptance_TRG13_StrategyLatency$' "$raw" 1 || failed=1
	sec="$(mktemp)"
	{
		section_head TRG-13 "bert scoring latency (TRG-13; INT8, in-process Score)"
		row "$raw" BenchmarkAcceptance_TRG13_StrategyLatency p99-ms "p99 (ms, median of the benchmark's runs)" "<" "$(threshold TRG-13 p99_ms_max)"
		row "$raw" BenchmarkAcceptance_TRG13_StrategyLatency p50-ms "p50 (ms)" "" ""
		row "$raw" BenchmarkAcceptance_TRG13_StrategyLatency p50-256tok-ms "p50 at 256 tokens (ms)" "" ""
		row "$raw" BenchmarkAcceptance_TRG13_StrategyLatency sat-req/s "saturation throughput (req/s)" "" ""
		row "$raw" BenchmarkAcceptance_TRG13_StrategyLatency sat256-req/s "saturation at 256 tokens (req/s)" "" ""
		echo
		echo "The benchmark performs its three runs internally and reports their medians; per-run values and"
		echo "min–max: \`acceptance/logs/TRG-13.json\`."
		echo "<!-- /scenario:TRG-13 -->"
	} >"$sec"
	replace_section TRG-13 "$sec"
	rm -f "$sec"
fi

if want micro; then
	raw="$out_dir/micro-$date_tag.txt"
	echo "bench: package micro-benchmarks x$count"
	go test -run '^$' -bench . -benchmem -count="$count" ./internal/... >"$raw" 2>&1 || failed=1
	sec="$(mktemp)"
	{
		echo "<!-- scenario:micro -->"
		echo "## Package micro-benchmarks — $(date +%Y-%m-%d)"
		machine_line
		echo
		echo '```text'
		(cd "$out_dir" && go tool benchstat "$(basename "$raw")")
		echo '```'
		echo "<!-- /scenario:micro -->"
	} >"$sec"
	replace_section micro "$sec"
	rm -f "$sec"
fi

if [ -f "$out_dir/trg02-$date_tag.txt" ] || [ -f "$out_dir/trg08-$date_tag.txt" ]; then
	cat "$out_dir"/trg0[28]-"$date_tag".txt >"$out_dir/benchstat-input.txt" 2>/dev/null || true
	go tool benchstat "$out_dir/benchstat-input.txt" >"$out_dir/benchstat-$date_tag.txt" 2>&1 || true
	echo "bench: benchstat summary in $out_dir/benchstat-$date_tag.txt"
fi
echo "bench: wrote $results"
exit "$failed"
