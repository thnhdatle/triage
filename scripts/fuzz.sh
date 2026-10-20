#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Runs every fuzz target for the given fuzztime (default 30s).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
fuzztime="${1:-30s}"
found=0
while IFS= read -r pkg; do
	[ -n "$pkg" ] || continue
	targets="$(go test -list 'Fuzz.*' "$pkg" 2>/dev/null | grep '^Fuzz' || true)"
	for t in $targets; do
		found=1
		echo "--- fuzz: $pkg $t ($fuzztime)"
		go test "$pkg" -run '^$' -fuzz "^${t}\$" -fuzztime "$fuzztime"
	done
done < <(go list ./... 2>/dev/null)
[ "$found" -eq 1 ] || echo "fuzz: no fuzz targets yet"
