#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Installs the pinned tools that make needs on a Linux CI runner.
set -euo pipefail

GOLANGCI_LINT_VERSION=2.14.0
HADOLINT_VERSION=2.15.1
GITLEAKS_VERSION=8.30.1
KUBECONFORM_VERSION=0.8.0
TRIVY_VERSION=0.76.0
SYFT_VERSION=1.54.1
YQ_VERSION=4.54.1
GORELEASER_VERSION=2.18.3
GOVULNCHECK_VERSION=1.9.0

BIN="${HOME}/.local/bin"
mkdir -p "$BIN"
echo "$BIN" >>"${GITHUB_PATH:-/dev/null}"
export PATH="$BIN:$PATH"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

case "$(uname -m)" in
x86_64 | amd64) goarch=amd64 uarch=x86_64 trivy_arch=64bit gitleaks_arch=x64 ;;
aarch64 | arm64) goarch=arm64 uarch=arm64 trivy_arch=ARM64 gitleaks_arch=arm64 ;;
*)
	echo "ci-install-tools: unsupported arch $(uname -m)" >&2
	exit 1
	;;
esac

gh_release() { echo "https://github.com/$1/releases/download/$2"; }

untar() {
	curl -fsSL --retry 3 "$1" | tar -xz -C "$tmp" "$2"
	install -m 0755 "$tmp/$2" "$BIN/$(basename "$2")"
}

raw() {
	curl -fsSL --retry 3 -o "$BIN/$2" "$1"
	chmod 0755 "$BIN/$2"
}

untar "$(gh_release golangci/golangci-lint "v${GOLANGCI_LINT_VERSION}")/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${goarch}.tar.gz" \
	"golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${goarch}/golangci-lint"
raw "$(gh_release hadolint/hadolint "v${HADOLINT_VERSION}")/hadolint-Linux-${uarch}" hadolint
untar "$(gh_release gitleaks/gitleaks "v${GITLEAKS_VERSION}")/gitleaks_${GITLEAKS_VERSION}_linux_${gitleaks_arch}.tar.gz" gitleaks
untar "$(gh_release yannh/kubeconform "v${KUBECONFORM_VERSION}")/kubeconform-linux-${goarch}.tar.gz" kubeconform
untar "$(gh_release aquasecurity/trivy "v${TRIVY_VERSION}")/trivy_${TRIVY_VERSION}_Linux-${trivy_arch}.tar.gz" trivy
untar "$(gh_release anchore/syft "v${SYFT_VERSION}")/syft_${SYFT_VERSION}_linux_${goarch}.tar.gz" syft
raw "$(gh_release mikefarah/yq "v${YQ_VERSION}")/yq_linux_${goarch}" yq
untar "$(gh_release goreleaser/goreleaser "v${GORELEASER_VERSION}")/goreleaser_Linux_${uarch}.tar.gz" goreleaser
GOBIN="$BIN" go install "golang.org/x/vuln/cmd/govulncheck@v${GOVULNCHECK_VERSION}"
if ! command -v shellcheck >/dev/null 2>&1; then
	sudo apt-get update -qq && sudo apt-get install -y -qq shellcheck >/dev/null
fi
