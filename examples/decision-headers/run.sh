#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Sends a short and a long prompt through the quickstart stack, prints the routing headers and
# looks up each decision on the admin listener.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=../lib.sh
source ../lib.sh
stack_up

ask() { # ask <label> <content>
	local headers body id
	headers="$(mktemp)"
	body="$(python3 -I -c 'import json,sys; print(json.dumps({"model":"triage-auto","messages":[{"role":"user","content":sys.argv[1]}]}))' "$2")"
	curl -fsS -D "$headers" -o /dev/null "$DATA/v1/chat/completions" -H 'Content-Type: application/json' -d "$body"
	echo "== $1"
	tr -d '\r' <"$headers" | grep -i -E '^x-triage-(route|tier|target):' | sort
	id="$(tr -d '\r' <"$headers" | sed -n 's/^[Xx]-[Tt]riage-[Dd]ecision: //p')"
	rm -f "$headers"
	curl -fsS "$ADMIN/v1/routing-decisions/$id" | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
print("decision: route=%s strategy=%s tier=%s attempts=%s" % (
    d["route"], d.get("strategy"), d.get("tier"), [a["target"] for a in d["attempts"]]))'
}

ask "short prompt" "Hello"
ask "long prompt" "$(python3 -I -c 'print("lorem ipsum " * 6000)')"
