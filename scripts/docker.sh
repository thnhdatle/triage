#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Builds the three local images for the host architecture.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*)
	echo "docker: unsupported host arch $(uname -m)" >&2
	exit 1
	;;
esac
plat="linux/$arch"
ver="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
args=(--platform "$plat" --build-arg "VERSION=$ver" --build-arg "COMMIT=$commit")
ort_key="linux_x64"
[ "$arch" = "arm64" ] && ort_key="linux_aarch64"
if [ -f models/manifest.yaml ] && command -v yq >/dev/null 2>&1; then
	ort_url="$(yq ".onnxruntime.$ort_key.url // \"\"" models/manifest.yaml)"
	ort_sha="$(yq ".onnxruntime.$ort_key.sha256 // \"\"" models/manifest.yaml)"
	if [ -n "$ort_url" ]; then
		args+=(--build-arg "ORT_URL=$ort_url" --build-arg "ORT_SHA256=$ort_sha")
	else
		echo "docker: notice: no onnxruntime pin for linux-$arch in models/manifest.yaml"
	fi
fi
docker build "${args[@]}" -f Dockerfile -t thnhdatle/triage:dev .
docker build "${args[@]}" -f Dockerfile.fakellm -t thnhdatle/fakellm:dev .
docker build --platform "$plat" -f Dockerfile.models -t thnhdatle/triage-models:dev .
echo "docker: built thnhdatle/{triage,fakellm,triage-models}:dev for $plat"
