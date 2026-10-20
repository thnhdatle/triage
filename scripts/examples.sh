#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Runs every examples/*/run.sh and compares stdout with expected.txt when present.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
shopt -s nullglob
runs=(examples/*/run.sh)
if [ "${#runs[@]}" -eq 0 ]; then
	echo "examples: none yet"
	exit 0
fi
rc=0
for r in "${runs[@]}"; do
	dir="$(dirname "$r")"
	echo "--- example: $dir"
	out="$(mktemp)"
	if ! (cd "$dir" && ./run.sh) >"$out"; then
		echo "examples: $dir failed" >&2
		rc=1
	elif [ -f "$dir/expected.txt" ] && ! diff -u "$dir/expected.txt" "$out"; then
		echo "examples: $dir output differs from expected.txt" >&2
		rc=1
	fi
	rm -f "$out"
done
exit "$rc"
