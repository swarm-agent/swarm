#!/usr/bin/env bash
# Purpose: the host publish gate must execute the exact candidate, not current
# folder HEAD, and reject script failure before minting credentials or pushing.
# Exercise publish-github.sh's actual gate using a real temporary Git repository.
set -euo pipefail
: "${TMPDIR:?TMPDIR required}"
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
testdir=$(mktemp -d "$TMPDIR/app-land-test.XXXXXX")
trap 'rm -rf -- "$testdir"' EXIT
mkdir -p "$testdir/repo/scripts"
git -C "$testdir/repo" init -q -b dev
git -C "$testdir/repo" config user.name 'App smoke'
git -C "$testdir/repo" config user.email app-smoke@example.invalid
printf 'printf "candidate: PASS\\n"\n' >"$testdir/repo/scripts/test-app-runs.sh"
git -C "$testdir/repo" add scripts/test-app-runs.sh
git -C "$testdir/repo" commit -qm candidate
head=$(git -C "$testdir/repo" rev-parse HEAD)
printf 'exit 99\n' >"$testdir/repo/scripts/test-app-runs.sh"
# Extract only the gate, not an alternative implementation or a fake publisher.
sed -n '/^  # This repository.s host-side app gate/,/^  permissions=/p' "$root/containers/headless/app/publish-github.sh" | sed '$d' >"$testdir/gate"
(
  REPO=swarm-agent/swarm
  folder=$testdir/repo
  git_folder() { git -C "$folder" "$@"; }
  die() { echo "$*" >&2; exit 1; }
  source "$testdir/gate"
)
git -C "$testdir/repo" add scripts/test-app-runs.sh
git -C "$testdir/repo" commit -qm failure
head=$(git -C "$testdir/repo" rev-parse HEAD)
if (
  REPO=swarm-agent/swarm
  folder=$testdir/repo
  git_folder() { git -C "$folder" "$@"; }
  die() { echo "$*" >&2; exit 1; }
  source "$testdir/gate"
); then echo 'landing accepted failing candidate' >&2; exit 1; fi
echo 'landing-candidate: PASS (exact commit and failure rejection)'
