# SPDX-License-Identifier: Apache-2.0
# Shared helpers for the examples: bring up the quickstart stack and tear it down on exit.
# Sourced by examples/*/run.sh; progress goes to stderr so stdout stays comparable.
# shellcheck shell=bash

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "$ROOT/deploy/docker-compose.yml")
export RAYLD_TRIAGE_IMAGE="${RAYLD_TRIAGE_IMAGE:-thnhdatle/triage:dev}"
export RAYLD_FAKELLM_IMAGE="${RAYLD_FAKELLM_IMAGE:-thnhdatle/fakellm:dev}"
export DATA=http://127.0.0.1:8080
export ADMIN=http://127.0.0.1:9091
export FAKE=http://127.0.0.1:8090

stack_down() { "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }

stack_up() {
	trap stack_down EXIT
	"${COMPOSE[@]}" up -d --wait >&2
}
