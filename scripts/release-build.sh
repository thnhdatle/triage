#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Builds the release archives, SBOMs and images for the host's Linux architecture.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

push=0 snapshot=0
for a in "$@"; do
	case "$a" in
	--push) push=1 ;;
	--snapshot) snapshot=1 ;;
	*)
		echo "usage: release-build.sh [--snapshot] [--push]" >&2
		exit 2
		;;
	esac
done
if [ "$(uname -s)" != Linux ]; then
	echo "release-build: needs a Linux host (triage is a CGO build, built natively per architecture)" >&2
	exit 1
fi
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*)
	echo "release-build: unsupported architecture $(uname -m)" >&2
	exit 1
	;;
esac

registry=ghcr.io/thnhdatle
source_url=https://github.com/thnhdatle/triage
out=dist/release
cfg="$(mktemp)"
trap 'rm -f "$cfg"' EXIT

yq '.builds[].goarch = ["'"$arch"'"]' .goreleaser.yaml >"$cfg"
if [ "$snapshot" = 1 ]; then
	goreleaser release --clean --snapshot -f "$cfg"
else
	goreleaser release --clean --skip=announce,publish -f "$cfg"
fi

json() { yq -p json -o yaml "$1" "$2"; }
version="$(json '.version' dist/metadata.json)"
mkdir -p "$out"
cp dist/*.tar.gz dist/*.sbom.spdx.json "$out/"

ctx=dist/image
for img in triage fakellm; do
	bin="$(json ".[] | select(.type == \"Binary\" and .extra.ID == \"$img\") | .path" dist/artifacts.json)"
	mkdir -p "$ctx/$img/linux/$arch"
	cp "$bin" "$ctx/$img/linux/$arch/$img"
done
scripts/stage-ort.sh "$ctx/triage/ort" "$arch"

for img in triage fakellm; do
	args=(
		--platform "linux/$arch" -f "Dockerfile.$img.release"
		--label "org.opencontainers.image.source=$source_url"
		--label "org.opencontainers.image.version=$version"
		--label "org.opencontainers.image.revision=$(git rev-parse HEAD)"
		--label "org.opencontainers.image.licenses=Apache-2.0"
	)
	if [ "$push" = 1 ]; then
		docker buildx build "${args[@]}" --sbom=true --metadata-file "$ctx/$img.json" \
			--output "type=image,name=$registry/$img,push-by-digest=true,name-canonical=true,push=true" "$ctx/$img"
		digest="$(json '."containerimage.digest"' "$ctx/$img.json")"
		mkdir -p "$out/digests/$img"
		touch "$out/digests/$img/${digest#sha256:}"
		echo "release-build: pushed $registry/$img@$digest (linux/$arch)"
	else
		docker buildx build "${args[@]}" --output type=cacheonly "$ctx/$img"
	fi
done
echo "release-build: linux/$arch artifacts in $out"
