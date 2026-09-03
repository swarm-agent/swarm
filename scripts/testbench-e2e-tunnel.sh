#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/testbench-e2e-tunnel.sh <check|run> [command [args...]]

Compatibility entrypoint for the canonical isolated container testbench.

Commands:
  check  Verify the broker-owned container is active on the exact current HEAD.
  run    Open temporary loopback forwards to the container and run a command with
         SWARM_DESKTOP_URL, SWARM_PRIMARY_API_URL, and SWARM_RUNNER_API_URL set.

This wrapper never probes, deploys, rebuilds, or restarts host swarm.service and
never uses host candidate ports 5555/7781. Deployment is owned exclusively by:
  scripts/testbench-container-deploy.sh deploy
USAGE
}

[[ $# -ge 1 ]] || { usage; exit 2; }
action="$1"
shift
case "$action" in
  check)
    [[ $# -eq 0 ]] || { usage; exit 2; }
    ;;
  run)
    [[ $# -gt 0 ]] || { usage; exit 2; }
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 2
    ;;
esac

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
exec "$root/scripts/testbench-container-deploy.sh" "$action" "$@"
