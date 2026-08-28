#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

ATLAS="docs/swarm-atlas.md"

fail() {
  echo "[atlas-sync] FAIL: $*" >&2
  exit 1
}

[[ -f "${ATLAS}" ]] || fail "missing canonical atlas at ${ATLAS}"

for heading in \
  '## 2. Evidence, authority, and revision policy' \
  '## 8. API catalog' \
  '## 9. Operational and release gates' \
  '## 10. Security- and durability-critical matrix' \
  '## 11. Test topology and mapping guidance' \
  '## 14. Revision ledger and update template'; do
  rg -q -F "${heading}" "${ATLAS}" || fail "atlas missing required section: ${heading}"
done

rg -q -F 'scripts/run-critical-tests.sh' "${ATLAS}" || fail "atlas does not name the canonical critical test runner"
rg -q -F 'scripts/check-atlas-sync.sh' "${ATLAS}" || fail "atlas does not name its synchronization gate"
rg -q -F 'docs/testing/test-audit-ledger.tsv' "${ATLAS}" || fail "atlas does not link the two-pass test audit ledger"
rg -q '^\| [0-9]+ \|' "${ATLAS}" || fail "atlas revision ledger has no recorded revision rows"

sensitive_changes="$({
  git diff --name-only --diff-filter=ACMRT HEAD -- \
    'swarmd/internal/api/*.go' \
    'swarmd/internal/session/*.go' \
    'swarmd/internal/store/pebble/*.go' \
    'swarmd/internal/agent/*.go' \
    'swarmd/internal/run/*.go' \
    'swarmd/internal/tool/*.go' \
    'swarmd/internal/permission/*.go' \
    'swarmd/internal/workspace/*.go' \
    'swarmd/internal/worktree/*.go' \
    'swarmd/internal/action/*.go' \
    'swarmd/internal/artifact/*.go' \
    'swarmd/internal/artifactgit/*.go' \
    'swarmd/internal/mediastaging/*.go' \
    'swarmd/internal/imagegen/*.go' \
    'swarmd/internal/htmlcapture/*.go' \
    'swarmd/internal/videoproject/*.go' \
    'swarmd/internal/videorender/*.go' \
    'swarmd/internal/provider/*.go' \
    'swarmd/internal/runtime/*.go' \
    'pkg/startupconfig/*.go' \
    'pkg/storagecontract/*.go' \
    'scripts/run-critical-tests.sh' \
    'scripts/check-precommit.sh' \
    'scripts/check-prepush.sh' \
    'scripts/check-launch-readiness.sh' \
    '.github/workflows/*.yml' \
    '.github/workflows/*.yaml' || true
  git ls-files --others --exclude-standard -- \
    'swarmd/internal/api/*.go' \
    'swarmd/internal/session/*.go' \
    'swarmd/internal/store/pebble/*.go' \
    'swarmd/internal/agent/*.go' \
    'swarmd/internal/run/*.go' \
    'swarmd/internal/tool/*.go' \
    'swarmd/internal/permission/*.go' \
    'swarmd/internal/workspace/*.go' \
    'swarmd/internal/worktree/*.go' \
    'swarmd/internal/action/*.go' \
    'swarmd/internal/artifact/*.go' \
    'swarmd/internal/artifactgit/*.go' \
    'swarmd/internal/mediastaging/*.go' \
    'swarmd/internal/imagegen/*.go' \
    'swarmd/internal/htmlcapture/*.go' \
    'swarmd/internal/videoproject/*.go' \
    'swarmd/internal/videorender/*.go' \
    'swarmd/internal/provider/*.go' \
    'swarmd/internal/runtime/*.go' \
    'pkg/startupconfig/*.go' \
    'pkg/storagecontract/*.go' \
    'scripts/run-critical-tests.sh' \
    'scripts/check-precommit.sh' \
    'scripts/check-prepush.sh' \
    'scripts/check-launch-readiness.sh' \
    '.github/workflows/*.yml' \
    '.github/workflows/*.yaml' || true
} | sed '/^$/d' | sort -u)"

if [[ -n "${sensitive_changes}" ]]; then
  atlas_changed=0
  if ! git diff --quiet HEAD -- "${ATLAS}"; then
    atlas_changed=1
  elif git ls-files --others --exclude-standard -- "${ATLAS}" | grep -q .; then
    atlas_changed=1
  fi
  if [[ "${atlas_changed}" != "1" ]]; then
    printf '[atlas-sync] sensitive architecture/security/test-gate changes require an atlas update in the same change:\n%s\n' "${sensitive_changes}" >&2
    fail "update ${ATLAS}, re-check affected citations, and append the revision ledger"
  fi
fi

echo "[atlas-sync] PASS"
