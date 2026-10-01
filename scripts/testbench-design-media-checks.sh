#!/usr/bin/env bash
# Bounded deterministic contract checks in the exact committed nspawn candidate.
# No provider requests, credentials, host services, or benchmark claims.
set -Eeuo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
: "${CANDIDATE_HEAD:?run only via the local testbench guest}"
[[ "$(git rev-parse HEAD)" == "$CANDIDATE_HEAD" ]]
export GOMAXPROCS=2 GOFLAGS=-p=2 RAYON_NUM_THREADS=2
export SWARM_TEST_BROWSER_CHANNEL=chrome
export GOCACHE_DIR="$(go env GOCACHE)" GOMODCACHE_DIR="$(go env GOMODCACHE)" GOPATH_DIR="$(go env GOPATH)"
run_check() {
  local label="$1"; shift
  local log
  log="$(mktemp "${TMPDIR:?}/design-check.XXXXXX")"
  local status=0
  timeout --kill-after=5s 180s "$@" 2>&1 | tail -c 6000 >"$log" || status=$?
  printf '\nCHECK %s exit=%s head=%s\n' "$label" "$status" "$CANDIDATE_HEAD"
  if (( status == 0 )); then tail -c 250 "$log"; else tail -c 6000 "$log"; fi
  return "$status"
}
run_check runtime python3 -B -m unittest discover -s scripts -p test_local_testbench_runtime.py -q
run_check fast bash scripts/run-critical-tests.sh fast
(cd swarmd && run_check store go test ./internal/store/pebble -run 'Test(ProjectDesign|DesignAcceptanceAtomicRecovery|DesignCatalog)' -count=1 -timeout=120s)
(cd swarmd && run_check api go test ./internal/api -run 'Test(ProjectDesignHTTP|DesignHTTP)' -count=1 -timeout=120s)
cd web
run_check state node --import tsx --test --test-concurrency=1 --test-timeout=30000 src/features/desktop/session-v3/design-api.spec.ts src/features/desktop/state/desktop-design-state.spec.ts src/features/desktop/runtime/desktop-design-runtime.spec.ts src/features/desktop/orchestrate/design-media-task.spec.ts
run_check types node ./scripts/use-local-node.mjs ./node_modules/typescript/bin/tsc --noEmit --incremental false
run_check browser node --import tsx --test --test-concurrency=1 --test-timeout=60000 src/features/desktop/orchestrate/design-media-center.browser.spec.ts src/features/desktop/tools/media-library/media-viewer-modal.browser.spec.ts
