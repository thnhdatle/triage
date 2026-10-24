#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Dry-runs the release build for this host without publishing.
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
