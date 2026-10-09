#!/usr/bin/env bash
# Agent sandbox suites against a real Docker engine (not part of the hermetic
# critical tiers):
#   1. the Go positive/negative suite (swarmd/internal/sandbox, SWARM_SANDBOX_E2E=1)
#   2. the red-team run through a real daemon (containers/sandbox/test/run.sh)
#
#   sudo bash scripts/test-sandbox.sh [--go-only|--redteam-only]
#
# Builds the sandbox image if missing and applies containers/sandbox/firewall.sh.
# SWARM_SANDBOX_E2E_IMAGE overrides the image for the Go suite (for example one
# that trusts a TLS-inspecting proxy's CA in a test environment).
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
mode=${1:-all}
[[ $EUID -eq 0 ]] || { echo "run as root (Docker, iptables and a test user are needed)" >&2; exit 1; }
docker image inspect swarm-sandbox:local >/dev/null 2>&1 || docker build -q -t swarm-sandbox:local "$repo/containers/sandbox" >/dev/null
bash "$repo/containers/sandbox/firewall.sh"
status=0
if [[ $mode != --redteam-only ]]; then
  echo "== Go sandbox suite"
  (cd "$repo/swarmd" && SWARM_SANDBOX_E2E=1 go test -count=1 -timeout 15m -run 'TestSandbox(Positive|Negative)Suite' -v ./internal/sandbox/ | grep -E '^(=== RUN|\s*--- |ok|FAIL|PASS)') || status=1
fi
if [[ $mode != --go-only ]]; then
  echo "== Red-team through a real daemon"
  bash "$repo/containers/sandbox/test/run.sh" || status=1
fi
exit $status
