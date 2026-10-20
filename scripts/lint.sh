#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# All lint steps of `make lint`. Prints which step failed.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
failed=()

step() {
	local name="$1"
	shift
	echo "--- lint: $name"
	if ! "$@"; then
		echo "lint: STEP FAILED: $name" >&2
		failed+=("$name")
	fi
}

gofmt_check() {
	local files out
	files="$(find . -name '*.go' -not -path './research/*' -not -path './node_modules/*')"
	[ -z "$files" ] && return 0
	# shellcheck disable=SC2086
	out="$(go tool gofumpt -l $files)" || return 1
	if [ -n "$out" ]; then
		echo "unformatted (run make fmt):" >&2
		echo "$out" >&2
		return 1
	fi
}

python_checks() {
	local d
	for d in research acceptance/sdk/python; do
		[ -f "$d/pyproject.toml" ] || continue
		(cd "$d" && uv run ruff format --check . && uv run ruff check . && uv run mypy --strict .) || return 1
	done
}

dockerfiles() {
	local f rc=0
	while IFS= read -r f; do
		hadolint "$f" || rc=1
	done < <(find . -name 'Dockerfile*' -not -path './research/.venv/*' -not -path './node_modules/*' -type f)
	return "$rc"
}

shell_checks() {
	local files
	files="$(ls scripts/*.sh .githooks/* 2>/dev/null)"
	# shellcheck disable=SC2086
	shellcheck $files
}

kube() {
	[ -d deploy/kubernetes ] || return 0
	kubeconform -strict -summary deploy/kubernetes
}

unreachable() {
	local out
	out="$(go tool deadcode -test ./...)" || return 1
	if [ -n "$out" ]; then
		echo "unreachable functions (remove or use them):" >&2
		echo "$out" >&2
		return 1
	fi
}

tracked_src() {
	git ls-files '*.go' '*.py' '*.sh'
}

todo_rule() {
	local hits
	hits="$(tracked_src | grep -v '^scripts/' | xargs grep -nE '\bTODO\b' 2>/dev/null | grep -v 'TODO(roadmap):' || true)"
	if [ -n "$hits" ]; then
		echo "bare TODO found (use TODO(roadmap): and list it in ROADMAP.md):" >&2
		echo "$hits" >&2
		return 1
	fi
}

spdx() {
	local f rc=0
	while IFS= read -r f; do
		[ -f "$f" ] || continue
		head -n 5 "$f" | grep -q 'SPDX-License-Identifier:' || {
			echo "missing SPDX header: $f" >&2
			rc=1
		}
	done < <(tracked_src)
	return "$rc"
}

step golangci-lint golangci-lint run
step gofumpt gofmt_check
step python python_checks
step hadolint dockerfiles
step shellcheck shell_checks
step gitleaks gitleaks detect --no-banner --redact
step kubeconform kube
step go-mod-tidy go mod tidy -diff
step deadcode unreachable
step todo-rule todo_rule
step spdx spdx

if [ "${#failed[@]}" -gt 0 ]; then
	echo "lint: failed steps: ${failed[*]}" >&2
	exit 1
fi
echo "lint: all steps passed"
