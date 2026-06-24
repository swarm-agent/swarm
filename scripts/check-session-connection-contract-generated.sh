#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

node scripts/generate-session-connection-contract.mjs
gofmt -w swarmd/internal/api/sessions_v3_connection_contract.generated.go

changed=()
for path in \
  swarmd/internal/api/sessions_v3_connection_contract.generated.go \
  web/src/features/desktop/session-connection/contract.generated.ts; do
  if ! git diff --quiet -- "${path}"; then
    changed+=("${path}")
  fi
done

if (( ${#changed[@]} > 0 )); then
  echo "[contract-generated] FAIL: generated session connection contract files are stale:" >&2
  printf '  %s\n' "${changed[@]}" >&2
  echo "[contract-generated] run: node scripts/generate-session-connection-contract.mjs && gofmt -w swarmd/internal/api/sessions_v3_connection_contract.generated.go" >&2
  exit 1
fi

echo "[contract-generated] PASS"
