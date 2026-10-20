#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Mutation testing with gremlins on the core routing and privacy packages; efficacy >= 60%.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
pkgs=(
	internal/domain/privacy
	internal/domain/difficulty
	internal/adapter/piiregex
	internal/app/route
	internal/app/dispatch
	internal/app/eval
)
rc=0
for p in "${pkgs[@]}"; do
	if [ ! -d "$p" ] || ! ls "$p"/*.go >/dev/null 2>&1; then
		echo "mutation: skip $p (no Go files)"
		continue
	fi
	echo "--- mutation: $p"
	go tool gremlins unleash --threshold-efficacy 60 "./$p" || {
		echo "mutation: FAILED $p" >&2
		rc=1
	}
done
exit "$rc"
