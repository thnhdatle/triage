#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Clones HEAD to a temp dir and runs the README quickstart commands verbatim,
# except `git clone` (the clone is done here). The local images are used instead of the released ones.
# Requires `make docker` to have built thnhdatle/triage:dev and thnhdatle/fakellm:dev.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
tmp="$(mktemp -d)"
cleanup() {
	if [ -f "$tmp/repo/deploy/docker-compose.yml" ]; then
		(cd "$tmp/repo" && docker compose -f deploy/docker-compose.yml down -v --remove-orphans >/dev/null 2>&1) || true
	fi
	rm -rf "${tmp:?}"
}
trap cleanup EXIT

git clone --quiet "$root" "$tmp/repo"
cmds="$tmp/quickstart.sh"
{
	echo '#!/usr/bin/env bash'
	echo 'set -euo pipefail'
	awk '/quickstart:start/{f=1;next} /quickstart:end/{f=0} f' "$tmp/repo/README.md" |
		awk '/^```/{inb=!inb;next} inb' |
		grep -v -e '^git clone '
} >"$cmds"
if [ "$(wc -l <"$cmds")" -le 2 ]; then
	echo "quickstart-test: no commands between quickstart markers in README.md" >&2
	exit 1
fi

export RAYLD_TRIAGE_IMAGE="${RAYLD_TRIAGE_IMAGE:-thnhdatle/triage:dev}"
export RAYLD_FAKELLM_IMAGE="${RAYLD_FAKELLM_IMAGE:-thnhdatle/fakellm:dev}"
start="$(date +%s)"
(cd "$tmp/repo" && bash "$cmds") | tee "$tmp/out.txt"
grep -qi '^HTTP/[0-9.]* 200' "$tmp/out.txt" || { echo "quickstart-test: no HTTP 200 in output" >&2; exit 1; }
grep -qi '^x-triage-decision:' "$tmp/out.txt" || { echo "quickstart-test: no X-Triage-Decision header" >&2; exit 1; }
echo "quickstart-test: elapsed $(($(date +%s) - start))s (limit 300s)"
