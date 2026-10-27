#!/bin/bash
# Runs the console's onboarding on your screen against the throwaway test VM
# (scripts/test-vm.sh up), installing Swarm there from a local release build
# (the output of scripts/build-main-dist.sh in a swarm-go checkout; this
# script copies the archive to the VM). Nothing touches the Swarm on this PC.
#   scripts/dogfood.sh SWARM_GO_CHECKOUT DIST_DIR
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
repo=${1:?dogfood needs a swarm-go checkout}
dist=${2:?dogfood needs its dist directory (scripts/build-main-dist.sh --output-dir DIR)}
installer=$repo/containers/headless/app/install.sh
[[ -f $installer ]] || { echo "no headless installer at $installer" >&2; exit 1; }
archive=$(ls "$dist"/swarm-v*-linux-amd64.tar.gz | head -1)
[[ -f $archive && -f $archive.sha256 ]] || { echo "no release archive (with .sha256) under $dist" >&2; exit 1; }
"$root/scripts/test-vm.sh" status | grep -q '^running' || { echo "start the VM first: scripts/test-vm.sh up" >&2; exit 1; }
key=$root/target/vm/id_ed25519
ssh_opts=(-i "$key" -p 4223 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)
name=$(basename "$archive")
if ! ssh "${ssh_opts[@]}" dev@127.0.0.1 test -f "/home/dev/swarm-dist/$name.sha256"; then
  echo "copying the release to the VM"
  tar -C "$dist" -cf - "$name" "$name.sha256" |
    ssh "${ssh_opts[@]}" dev@127.0.0.1 'mkdir -p ~/swarm-dist && tar -C ~/swarm-dist -xf -'
fi
cd "$root"
exec env SWARM_CONSOLE_ONBOARDING=1 \
  SWARM_CONSOLE_SSH=dev@127.0.0.1:4223 \
  SWARM_CONSOLE_SSH_KEY="$key" \
  SWARM_CONSOLE_INSTALLER="$installer" \
  SWARM_CONSOLE_INSTALL_ARGS="--release-file /home/dev/swarm-dist/$name" \
  cargo run
