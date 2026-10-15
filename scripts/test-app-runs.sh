#!/usr/bin/env bash
# Real-process smoke; all disposable state stays under the caller's TMPDIR.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
exec node "$root/scripts/test-app-runs.mjs" "$@"
