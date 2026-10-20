#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Prints the CHANGELOG.md section of a release: release-notes.sh v0.1.0
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
ver="${1:?usage: release-notes.sh vX.Y.Z}"
ver="${ver#v}"
notes="$(awk -v v="$ver" '
	$0 ~ "^## \\[" v "\\]" { f = 1; next }
	f && /^## \[/ { exit }
	f && /^\[[^]]+\]: / { exit }
	f { print }
' CHANGELOG.md)"
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
	echo "release-notes: no CHANGELOG.md section for $ver" >&2
	exit 1
fi
printf '%s\n' "$notes"
