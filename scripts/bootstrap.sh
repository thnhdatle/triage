#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Checks tools, downloads modules, sets hooks, fetches pinned artifacts. Installs nothing globally.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
CACHE_DIR="${CACHE_DIR:-$HOME/.cache/rayld/triage}"
MANIFEST=models/manifest.yaml

missing=0
for t in go golangci-lint uv docker jq yq shellcheck hadolint gitleaks trivy goreleaser node python3.12; do
	if command -v "$t" >/dev/null 2>&1; then
		v="$("$t" --version 2>&1 | head -n1 || true)"
		echo "ok       $t ($v)"
	else
		echo "MISSING  $t"
		missing=1
	fi
done
[ "$missing" -eq 0 ] || echo "bootstrap: some tools are missing (see above); see CONTRIBUTING.md for the toolchain" >&2

go mod download
git config core.hooksPath .githooks
if [ -f research/pyproject.toml ]; then
	(cd research && uv sync)
fi

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

fetch() {
	local url="$1" want="$2" dest="$3" tmp got
	if [ -f "$dest" ] && [ "$(sha256_of "$dest")" = "$want" ]; then
		echo "cached   $dest"
		return 0
	fi
	mkdir -p "$(dirname "$dest")"
	tmp="$(mktemp "$dest.XXXXXX")"
	if ! curl -fsSL --retry 3 -o "$tmp" "$url"; then
		rm -f "$tmp"
		echo "bootstrap: download failed: $url" >&2
		return 1
	fi
	got="$(sha256_of "$tmp")"
	if [ "$got" != "$want" ]; then
		rm -f "$tmp"
		echo "bootstrap: SHA-256 mismatch for $url (want $want, got $got)" >&2
		return 1
	fi
	mv "$tmp" "$dest"
	echo "fetched  $dest"
}

if [ -f "$MANIFEST" ] && command -v yq >/dev/null 2>&1; then
	for name in bert ner; do
		repo="$(yq ".entries[] | select(.name == \"$name\") | .hf_repo" "$MANIFEST")"
		rev="$(yq ".entries[] | select(.name == \"$name\") | .revision" "$MANIFEST")"
		path="$(yq ".entries[] | select(.name == \"$name\") | .files[] | select(.path == \"tokenizer.json\" or .path == \"onnx/tokenizer.json\") | .path" "$MANIFEST")"
		sha="$(yq ".entries[] | select(.name == \"$name\") | .files[] | select(.path == \"$path\") | .sha256" "$MANIFEST")"
		fetch "https://huggingface.co/$repo/resolve/$rev/$path" "$sha" "$CACHE_DIR/tokenizers/$name/tokenizer.json"
	done
else
	echo "notice: $MANIFEST or yq not available; skipping pinned artifact fetch"
fi

if [ -f deploy/docker-compose.yml ] && command -v docker >/dev/null 2>&1; then
	docker compose -f deploy/docker-compose.yml pull --ignore-buildable ||
		echo "notice: compose pull failed (images may not be published yet); continuing"
fi
echo "bootstrap done"
