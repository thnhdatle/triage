#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# govulncheck, go-licenses, trivy on local images.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
govulncheck ./...
go tool go-licenses check ./... \
	--ignore github.com/thnhdatle/triage \
	--allowed_licenses=Apache-2.0,MIT,MIT-CMU,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0,0BSD,Zlib,PSF-2.0,Unlicense,CC0-1.0
for img in thnhdatle/triage:dev thnhdatle/fakellm:dev; do
	if docker image inspect "$img" >/dev/null 2>&1; then
		trivy image --severity CRITICAL,HIGH --ignore-unfixed --exit-code 1 "$img"
	else
		echo "security: image $img not present locally, skipping trivy (run make docker)"
	fi
done
