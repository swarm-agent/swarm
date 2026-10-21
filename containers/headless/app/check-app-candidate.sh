#!/usr/bin/env bash
# The publisher is a privileged credential boundary, not a candidate executor.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
if grep -q 'test-app-runs\|git_folder archive' "$root/containers/headless/app/publish-github.sh"; then
  echo 'publisher-host-isolation: FAIL'; exit 1
fi
bash -n "$root/containers/headless/app/publish-github.sh"
echo 'publisher-host-isolation: PASS (static guard; sandbox execution tested separately)'
