#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Sends an e-mail address in a long prompt that is routed to the cloud target and shows, from the
# provider's request log, that the cloud target received a placeholder instead of the address.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=../lib.sh
source ../lib.sh
stack_up

email="ana.example@example.com"
body="$(python3 -I -c '
import json, sys
text = "Please reply to " + sys.argv[1] + ". " + "lorem ipsum " * 6000
print(json.dumps({"model": "triage-auto", "messages": [{"role": "user", "content": text}]}))' "$email")"

curl -fsS -D - -o /dev/null "$DATA/v1/chat/completions" -H 'Content-Type: application/json' -d "$body" |
	tr -d '\r' | grep -i -E '^x-triage-(route|tier|target):' | sort

curl -fsS "$FAKE/_fake/requests" | python3 -I -c '
import json, sys
email = sys.argv[1]
for r in json.load(sys.stdin)["requests"]:
    if r["kind"] != "chat":
        continue
    print("upstream model:", r["model"])
    print("contains the address:", email in r["body"])
    print("contains [[EMAIL_1]]:", "[[EMAIL_1]]" in r["body"])' "$email"
