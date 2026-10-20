#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Release dry run of what this host can build, without publishing. On Linux: the snapshot archives,
# SBOMs and both release images for the host architecture (scripts/release-build.sh --snapshot).
# On macOS, triage (CGO) cannot be cross-built for Linux: the config is checked, fakellm is built for
# linux/<host arch>, and the server image is built from Dockerfile for linux/<host arch>.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
if [ "$(uname -s)" = Linux ]; then
	exec scripts/release-build.sh --snapshot
fi
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
*) arch=arm64 ;;
esac
goreleaser check
GOOS=linux GOARCH="$arch" goreleaser build --snapshot --clean --single-target --id fakellm
docker buildx build --platform "linux/$arch" -f Dockerfile . --output type=cacheonly
