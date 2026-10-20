#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Publishes a release from the outputs of scripts/release-build.sh --push on every architecture,
# collected in dist/release/: merges the per-architecture images into multi-arch manifests tagged
# <tag> and latest, writes checksums.txt, and creates the GitHub release with the CHANGELOG.md
# section of <tag> as notes. Needs docker buildx (logged in to ghcr.io) and gh (GH_TOKEN).
# Usage: release-publish.sh <tag>
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
tag="${1:?usage: release-publish.sh <tag>}"
registry=ghcr.io/thnhdatle
dir=dist/release
archs=2

for img in triage fakellm; do
	refs=()
	for f in "$dir/digests/$img"/*; do
		[ -e "$f" ] && refs+=("$registry/$img@sha256:$(basename "$f")")
	done
	if [ "${#refs[@]}" -ne "$archs" ]; then
		echo "release-publish: expected $archs $img digests, found ${#refs[@]}" >&2
		exit 1
	fi
	docker buildx imagetools create -t "$registry/$img:$tag" -t "$registry/$img:latest" "${refs[@]}"
	docker buildx imagetools inspect "$registry/$img:$tag"
done

shopt -s nullglob
archives=("$dir"/*.tar.gz)
sboms=("$dir"/*.sbom.spdx.json)
if [ "${#archives[@]}" -ne $((2 * archs)) ] || [ "${#sboms[@]}" -ne "${#archives[@]}" ]; then
	echo "release-publish: expected $((2 * archs)) archives with SBOMs, found ${#archives[@]} and ${#sboms[@]}" >&2
	exit 1
fi
(cd "$dir" && sha256sum -- *.tar.gz *.sbom.spdx.json >checksums.txt)

notes="$(mktemp)"
trap 'rm -f "$notes"' EXIT
scripts/release-notes.sh "$tag" >"$notes"
gh release create "$tag" --verify-tag --title "$tag" --notes-file "$notes" \
	"${archives[@]}" "${sboms[@]}" "$dir/checksums.txt"
