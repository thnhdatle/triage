#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Pre-release checks, run by `make release-check` before tagging.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
fail=0
bad() {
	echo "release-check: FAIL: $*" >&2
	fail=1
}
ok() { echo "release-check: ok: $*"; }

# 1. only project files tracked: these hidden files and directories, and these top-level Markdown files
hidden_ok='^\.(dockerignore|editorconfig|gitignore|gitleaks\.toml|golangci\.yml|goreleaser\.yaml|githooks/.+|github/.+)$'
md_ok='^(README|CHANGELOG|CONTRIBUTING|CODE_OF_CONDUCT|SECURITY|ROADMAP)\.md$'
extra="$(git ls-files | grep -E '^\.' | grep -vE "$hidden_ok" || true)$(git ls-files '*.md' | grep -v / | grep -vE "$md_ok" || true)"
if [ -n "$extra" ]; then bad "unexpected files tracked: $extra"; else ok "only project files tracked"; fi

# 2. commit messages: one-line Conventional Commits
re='^(feat|fix|perf|refactor|test|docs|build|ci|chore)(\([a-z0-9-]+\))?!?: [a-z0-9].{0,70}[^.]$'
msgbad=0
while IFS= read -r sha; do
	body="$(git log -1 --format=%B "$sha" | sed '/^[[:space:]]*$/d')"
	if [ "$(printf '%s\n' "$body" | wc -l | tr -d ' ')" -ne 1 ] || ! printf '%s' "$body" | grep -Eq "$re" || [ "${#body}" -gt 72 ]; then
		bad "commit $sha message invalid: $(printf '%s' "$body" | head -n1)"
		msgbad=1
	fi
done < <(git log --format=%H)
[ "$msgbad" -eq 0 ] && ok "commit messages are one-line Conventional Commits"

# 3. SPDX headers
spdxbad=0
while IFS= read -r f; do
	[ -f "$f" ] || continue
	head -n 5 "$f" | grep -q 'SPDX-License-Identifier:' || {
		bad "missing SPDX header: $f"
		spdxbad=1
	}
done < <(git ls-files '*.go' '*.py' '*.sh')
[ "$spdxbad" -eq 0 ] && ok "SPDX headers present"

# 4. TODO rule
todos="$(git ls-files '*.go' '*.py' | xargs grep -nE '\bTODO\b' 2>/dev/null | grep -v 'TODO(roadmap):' || true)"
if [ -n "$todos" ]; then bad "bare TODO: $todos"; else ok "no bare TODO"; fi

# 5. community files
for f in README.md LICENSE NOTICE CHANGELOG.md ROADMAP.md CONTRIBUTING.md CODE_OF_CONDUCT.md SECURITY.md \
	.github/CODEOWNERS .github/dependabot.yml .github/PULL_REQUEST_TEMPLATE.md \
	.github/ISSUE_TEMPLATE/bug_report.yml .github/ISSUE_TEMPLATE/feature_request.yml; do
	[ -f "$f" ] || bad "missing community file: $f"
done

# 6. every TODO(roadmap) reference is present in ROADMAP.md
refs="$(git ls-files '*.go' '*.py' | xargs grep -hoE 'TODO\(roadmap\): *.*' 2>/dev/null || true)"
if [ -n "$refs" ]; then
	if [ ! -f ROADMAP.md ]; then
		bad "TODO(roadmap) present but ROADMAP.md missing"
	else
		while IFS= read -r line; do
			key="$(printf '%s' "${line#TODO(roadmap):}" | sed -E 's/^ +//')"
			id="$(printf '%s' "$key" | grep -oE '^[A-Za-z0-9_.-]+' || true)"
			if [ -n "$id" ] && ! grep -qF "$id" ROADMAP.md; then bad "ROADMAP.md lacks entry for TODO(roadmap): $key"; fi
		done <<<"$refs"
	fi
fi

# 7. CHANGELOG has a dated section for the release in the Makefile
release="$(sed -nE 's/^RELEASE[[:space:]]*\?=[[:space:]]*v?([0-9.]+)$/\1/p' Makefile)"
if grep -Eq "^## \[${release//./\\.}\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$" CHANGELOG.md; then
	ok "CHANGELOG section ${release} is dated"
else
	bad "CHANGELOG.md has no dated '## [${release}] - YYYY-MM-DD' section"
fi

# 8. every FAIL row of the acceptance report is explained in acceptance/not_met.yaml
unexplained=0
while IFS= read -r id; do
	grep -q "^${id}:" acceptance/not_met.yaml 2>/dev/null || {
		bad "acceptance FAIL row without explanation: $id"
		unexplained=1
	}
done < <(sed -nE 's/^\| ([A-Za-z0-9-]+) \| FAIL \|.*/\1/p' acceptance/report.md)
[ "$unexplained" -eq 0 ] && ok "acceptance FAIL rows are explained"

[ "$fail" -eq 0 ] && echo "release-check: all automated checks passed"
exit "$fail"
