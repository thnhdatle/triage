#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Coverage with thresholds: whole repo >= 80%, internal/domain + internal/app >= 90%.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
OUT=coverage.out
pkgs="$(go list ./... | grep -Ev '/(cmd|bench|acceptance|examples|research)(/|$)' || true)"
if [ -z "$pkgs" ]; then
	echo "coverage: no packages yet; whole repo n/a, domain+app n/a"
	exit 0
fi
coverpkg="$(echo "$pkgs" | tr '\n' ',' | sed 's/,$//')"
tags=()
[ "${INTEGRATION:-0}" = "1" ] && tags=(-tags integration)
# shellcheck disable=SC2086
go test -race -count=1 ${tags[@]+"${tags[@]}"} -coverpkg="$coverpkg" -coverprofile="$OUT.raw" $pkgs
# drop generated files from the profile
grep -Ev '(_gen\.go|\.pb\.go):' "$OUT.raw" >"$OUT"
rm -f "$OUT.raw"

pct() { # pct <profile>: statement coverage in percent, "n/a" when no statements
	# -coverpkg repeats every block once per test binary: merge by block, covered if any run hit it.
	awk 'NR>1 { st[$1]=$2; if ($3>0) hit[$1]=1 }
		END { for (b in st) { n+=st[b]; if (b in hit) c+=st[b] }
			if (n==0) print "n/a"; else printf "%.1f", 100*c/n }' "$1"
}
total="$(pct "$OUT")"
{
	head -n1 "$OUT"
	grep -E 'internal/(domain|app)/' "$OUT" || true
} >coverage.core.out
core="$(pct coverage.core.out)"
rm -f coverage.core.out
echo "coverage: whole repo = ${total}% (min 80)"
echo "coverage: internal/domain + internal/app = ${core}% (min 90)"
rc=0
if [ "$total" != "n/a" ] && awk -v v="$total" 'BEGIN{exit !(v<80)}'; then
	echo "coverage: whole repo below 80%" >&2
	rc=1
fi
if [ "$core" != "n/a" ] && awk -v v="$core" 'BEGIN{exit !(v<90)}'; then
	echo "coverage: domain+app below 90%" >&2
	rc=1
fi
exit "$rc"
