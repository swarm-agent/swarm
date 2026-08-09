#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/run-desktop-launch-test.sh <ssh-alias> <provider> [--desktop-url <url>] [--workspace <name-or-path>] [--timeout-ms <ms>] [--headful]

Runs the canonical Playwright Desktop launch suite against an already-running
Swarm target. The SSH alias, provider, and Desktop URL are invocation inputs;
the suite does not persist them.

Arguments:
  ssh-alias     SSH alias used when --desktop-url is omitted
  provider      Provider id whose catalog recommendations select Plan and Auto models

Options:
  --desktop-url  Direct served Desktop URL. When omitted, Playwright uses an SSH
                 tunnel to the target's loopback Desktop listener.
  --workspace    Saved workspace name or path (default: first saved workspace)
  --timeout-ms   Per-lifecycle wait budget (default: 900000)
  --headful      Show the Playwright browser

Examples:
  scripts/run-desktop-launch-test.sh runner-alias provider-id
  scripts/run-desktop-launch-test.sh runner-alias provider-id --desktop-url <served-desktop-url>
USAGE
}

fail() {
  printf 'run-desktop-launch-test: %s\n' "$*" >&2
  exit 1
}

if [[ $# -eq 1 && ( "$1" == "-h" || "$1" == "--help" ) ]]; then
  usage
  exit 0
fi
[[ $# -ge 2 ]] || { usage; exit 2; }

SSH_ALIAS="$1"
PROVIDER="$2"
shift 2

DESKTOP_URL=""
WORKSPACE=""
TIMEOUT_MS="900000"
HEADFUL="0"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --desktop-url)
      [[ $# -ge 2 ]] || fail "--desktop-url requires a value"
      DESKTOP_URL="$2"
      shift 2
      ;;
    --workspace)
      [[ $# -ge 2 ]] || fail "--workspace requires a value"
      WORKSPACE="$2"
      shift 2
      ;;
    --timeout-ms)
      [[ $# -ge 2 ]] || fail "--timeout-ms requires a value"
      TIMEOUT_MS="$2"
      shift 2
      ;;
    --headful)
      HEADFUL="1"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ -n "${SSH_ALIAS}" ]] || fail "ssh-alias must not be empty"
[[ "${PROVIDER}" =~ ^[A-Za-z0-9._-]+$ ]] || fail "provider contains unsupported characters"
[[ "${TIMEOUT_MS}" =~ ^[0-9]+$ ]] || fail "--timeout-ms must be an integer"
(( TIMEOUT_MS >= 30000 )) || fail "--timeout-ms must be at least 30000"
if [[ -n "${DESKTOP_URL}" ]]; then
  [[ "${DESKTOP_URL}" =~ ^https?://[^[:space:]]+$ ]] || fail "--desktop-url must be an http(s) URL"
fi

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="${ROOT_DIR}/web"
SPEC="./src/features/desktop/chat/components/desktop-launch-canonical.e2e.spec.ts"
[[ -d "${WEB_DIR}/node_modules/playwright" ]] || fail "Playwright is unavailable; install web dependencies first"

cd "${WEB_DIR}"
exec env \
  SWARM_DESKTOP_LAUNCH_E2E=1 \
  SWARM_PRIMARY_SSH="${SSH_ALIAS}" \
  SWARM_DESKTOP_URL="${DESKTOP_URL}" \
  SWARM_PROVIDER="${PROVIDER}" \
  SWARM_E2E_WORKSPACE="${WORKSPACE}" \
  SWARM_DESKTOP_LAUNCH_TIMEOUT_MS="${TIMEOUT_MS}" \
  SWARM_E2E_HEADFUL="${HEADFUL}" \
  node --import tsx --test --test-force-exit "${SPEC}"
