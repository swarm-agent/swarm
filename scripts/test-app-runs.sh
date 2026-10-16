#!/usr/bin/env bash
# Real-process smoke; all disposable state stays under TMPDIR.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
if [[ -z ${TMPDIR:-} ]]; then
  TMPDIR=$(mktemp -d)
  export TMPDIR
  trap 'rm -rf -- "$TMPDIR"' EXIT
  # Keep the shell alive so its EXIT trap also runs when node fails.
  node "$root/scripts/test-app-runs.mjs" "$@"
else
  exec node "$root/scripts/test-app-runs.mjs" "$@"
fi
