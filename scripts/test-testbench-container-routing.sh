#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
lib="$root/scripts/lib-testbench-e2e.sh"
container="$root/scripts/testbench-container-deploy.sh"
compat="$root/scripts/testbench-e2e-tunnel.sh"
runner="$root/scripts/run-testbench-runner.sh"
host="$root/scripts/ssh-fast-test.sh"
example="$root/.env.example"
doc="$root/docs/testing/testbench-container.md"

require_literal() {
  local file="$1" text="$2"
  grep -Fq -- "$text" "$file" || { printf 'missing %s in %s\n' "$text" "$file" >&2; exit 1; }
}
for file in "$lib" "$container" "$compat" "$runner" "$host" "$example" "$doc"; do
  [[ -f "$file" ]] || { printf 'missing %s\n' "$file" >&2; exit 1; }
done

require_literal "$example" 'SWARM_TESTBENCH_TARGET=container'
require_literal "$example" 'SWARM_REMOTE_DESKTOP_PORT=5655'
require_literal "$example" 'SWARM_TESTBENCH_REMOTE_API_PORT=7881'
require_literal "$lib" 'SWARM_TESTBENCH_TARGET must be container'
require_literal "$lib" 'host ports 5555/7781 are retired for live candidate testing'
require_literal "$container" 'broker_action test-container-prepare'
require_literal "$container" 'broker_action test-container-start'
require_literal "$container" 'container is stopped or stale; deploying current committed HEAD'
require_literal "$container" 'export SWARM_RUNNER_API_URL="$SWARM_DESKTOP_URL"'
require_literal "$compat" 'exec "$root/scripts/testbench-container-deploy.sh" "$action" "$@"'
require_literal "$runner" 'exec "${ROOT_DIR}/scripts/testbench-container-deploy.sh" run "${runner[@]}"'
require_literal "$host" 'use scripts/testbench-container-deploy.sh deploy instead of host swarm.service'
require_literal "$doc" 'The host Swarm service and host candidate ports are not a fallback.'

if grep -Eq 'ssh-fast-test\.sh|swarm-service-(reload|restart)|127\.0\.0\.1:(5555|7781)' "$compat"; then
  printf 'compatibility tunnel retains a host-service fallback\n' >&2
  exit 1
fi

printf 'container testbench routing contract: ok\n'
