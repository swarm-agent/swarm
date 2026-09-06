#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/run-testbench-desktop-e2e.sh [--workspace <name-or-path>] [--timeout-ms <ms>] [--headful]

Loads the ignored repository-root .env, opens the configured loopback-only SSH
forwards, and runs the canonical Desktop launch E2E against that exact alias.
USAGE
}

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "${ROOT_DIR}/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "${ROOT_DIR}" || exit 1
swarm_testbench_validate_env || exit 1

if [[ -z "${PLAYWRIGHT_BROWSER_EXECUTABLE:-}" ]]; then
  for candidate in google-chrome-stable google-chrome chromium chromium-browser; do
    if command -v "${candidate}" >/dev/null 2>&1; then
      PLAYWRIGHT_BROWSER_EXECUTABLE="$(command -v "${candidate}")"
      export PLAYWRIGHT_BROWSER_EXECUTABLE
      break
    fi
  done
fi

export SWARM_E2E_ACTION_MODEL="${SWARM_TESTBENCH_ACTION_MODEL}"
export SWARM_E2E_ACTION_THINKING="${SWARM_TESTBENCH_ACTION_THINKING}"
export SWARM_E2E_PLAN_MODEL="${SWARM_TESTBENCH_PLAN_MODEL}"
export SWARM_E2E_PLAN_THINKING="${SWARM_TESTBENCH_PLAN_THINKING}"
export SWARM_DESKTOP_FIRST_MESSAGE_E2E="${SWARM_DESKTOP_FIRST_MESSAGE_E2E:-0}"

case "${1:-}" in
  -h|--help) usage; exit 0 ;;
esac

exec "${ROOT_DIR}/scripts/testbench-e2e-tunnel.sh" run \
  "${ROOT_DIR}/scripts/run-desktop-launch-test.sh" \
  "${SWARM_PRIMARY_SSH}" \
  "${SWARM_TESTBENCH_PROVIDER}" \
  --desktop-url "http://127.0.0.1:${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}" \
  "$@"
