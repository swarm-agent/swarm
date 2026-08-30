#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/sync-testbench-model-config.sh

Renders the ignored repository-root .env's non-secret Fireworks model posture,
installs it at the fixed pending path on the configured testbench alias, and
invokes the fixed broker action that validates and promotes it to root-owned
release-gate configuration. This does not run tests or expose credentials.
USAGE
}

fail() {
  printf 'sync-testbench-model-config: %s\n' "$*" >&2
  exit 1
}

if (($# > 0)); then
  case "$1" in
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
fi

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "${ROOT_DIR}/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "${ROOT_DIR}" || exit 1
swarm_testbench_validate_env || exit 1
command -v ssh >/dev/null 2>&1 || fail 'ssh is required'

config="$(mktemp "${TMPDIR:?TMPDIR must be set}/testbench-model-config.XXXXXX")"
cleanup() { : >"${config}"; rm -f -- "${config}"; }
trap cleanup EXIT INT TERM
"${ROOT_DIR}/scripts/render-testbench-model-config.sh" >"${config}"
chmod 0600 "${config}"

ssh "${SWARM_PRIMARY_SSH}" 'set -euo pipefail; install -d -m 0700 /var/cache/swarm-testbench-model-config; umask 077; cat >/var/cache/swarm-testbench-model-config/pending.conf; chmod 0600 /var/cache/swarm-testbench-model-config/pending.conf' <"${config}"
ssh "${SWARM_PRIMARY_SSH}" 'sudo -n /usr/local/sbin/swarm-testbench-admin gate-sync-models'
printf 'sync-testbench-model-config: testbench Fireworks model configuration synchronized\n'
