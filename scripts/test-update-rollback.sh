#!/usr/bin/env bash
# Purpose: exercise the installer's actual acceptance/rollback decision with
# real installed executables and symlinks. A failing candidate must restore the
# previous executable and report rolled_back, not installed. Systemd/Docker
# activation is outside this unprivileged filesystem test.
set -euo pipefail
: "${TMPDIR:?TMPDIR required}"
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d "$TMPDIR/app-rollback.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
OPT=$work/install
UPDATE_BAD=$work/bad
mkdir -p "$OPT/releases/old/bin" "$OPT/releases/new/bin"
printf '#!/bin/sh\nexit 0\n' >"$OPT/releases/old/bin/swarmd"
printf '#!/bin/sh\nexit 1\n' >"$OPT/releases/new/bin/swarmd"
chmod +x "$OPT"/releases/*/bin/swarmd
ln -s "$OPT/releases/new" "$OPT/current"
sed -n '/^verify_update_or_rollback() {/,/^}/p' "$root/containers/headless/app/install.sh" >"$work/decision"
source "$work/decision"
sed -n '/^post_update_smoke() {/,/^}/p' "$root/containers/headless/app/install.sh" >"$work/smoke"
sed -n '/^local_api()/p' "$root/containers/headless/app/install.sh" >>"$work/smoke"
source "$work/smoke"
# Real process execution represents the smoke outcome of each installed build.
came_up() {
  if [[ -n ${1:-} && -n ${SMOKE_SOCKET:-} ]]; then
    LOCAL_API_SOCKET=$SMOKE_SOCKET
    [[ $(readlink "$OPT/current") == "$OPT/releases/new" ]] && LOCAL_API_SOCKET=$work/unavailable.sock
    post_update_smoke
  else
    "$OPT/current/bin/swarmd"
  fi
}
SMOKE_SOCKET=${1:-}
activate() { ln -sfn "$OPT/releases/$1" "$OPT/current.new" && mv -T "$OPT/current.new" "$OPT/current"; }
update_state() { printf '%s: %s\n' "$1" "$2"; }
output=$(verify_update_or_rollback new old false)
[[ $(readlink "$OPT/current") == "$OPT/releases/old" ]]
[[ $(cat "$UPDATE_BAD") == new ]]
[[ $output == *'rolled_back: new did not come up; back on old'* ]]
"$OPT/current/bin/swarmd"
printf '%s\nupdate-rollback: PASS\n' "$output"
