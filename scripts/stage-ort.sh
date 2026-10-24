#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Downloads and stages the pinned ONNX Runtime library into <dest>.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dest="${1:?usage: stage-ort.sh <dest> [amd64|arm64]}"
arch="${2:-$(uname -m)}"
case "$arch" in
x86_64 | amd64) key=linux_x64 ;;
aarch64 | arm64) key=linux_aarch64 ;;
*)
	echo "stage-ort: unsupported arch $arch" >&2
	exit 1
	;;
esac
url="$(yq ".onnxruntime.${key}.url" models/manifest.yaml)"
want="$(yq ".onnxruntime.${key}.sha256" models/manifest.yaml)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp/ort.tgz" "$url"
if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$tmp/ort.tgz" | awk '{print $1}')"
else
	got="$(shasum -a 256 "$tmp/ort.tgz" | awk '{print $1}')"
fi
if [ "$got" != "$want" ]; then
	echo "stage-ort: SHA-256 mismatch for $url (want $want, got $got)" >&2
	exit 1
fi
mkdir -p "$tmp/x" "$dest"
tar -xzf "$tmp/ort.tgz" -C "$tmp/x" --strip-components=1
cp -L "$tmp/x/lib/libonnxruntime.so" "$dest/libonnxruntime.so"
cp "$tmp/x/LICENSE" "$dest/LICENSE"
cp "$tmp/x/ThirdPartyNotices.txt" "$dest/ThirdPartyNotices.txt"
echo "stage-ort: ONNX Runtime $(cat "$tmp/x/VERSION_NUMBER") for $key in $dest"
