#!/usr/bin/env bash
# Explicit hermetic PR selection. Live/paid/browser journeys are opt-in elsewhere.
# Orchestrator remains separate from the curated critical manifest until reviewed.
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if (( $# != 0 )); then echo 'PR checks accept no extra selectors' >&2; exit 2; fi
export GOMAXPROCS=2 GOFLAGS=-p=2 RAYON_NUM_THREADS=2
# Existing gate retains session API, auth/hydration/realtime, backend and TUI proofs.
bash scripts/run-critical-tests.sh fast
node --test --test-concurrency=1 --test-timeout=10000 scripts/runners/orchestrator-pr.test.mjs scripts/run-new-task-smoke.test.mjs scripts/run-new-task-smoke-isolated.test.mjs
source scripts/lib-pnpm.sh
(cd web; swarm_pnpm run test:orchestrator:selection; swarm_pnpm run test:orchestrator)
