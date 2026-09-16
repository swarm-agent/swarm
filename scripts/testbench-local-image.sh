#!/usr/bin/env bash
# Provision a bounded base filesystem; never installs or restarts host Swarm.
set -euo pipefail
usage() {
  printf '%s\n' 'Usage: bash scripts/testbench-local-image.sh IMAGE SIZE_MIB MIRROR' \
    'Creates a new ext4 image with a minimal Ubuntu resolute rootfs.' \
    'Requires root, debootstrap, mkfs.ext4, mount and an existing private parent.' \
    'Refuses replacement. Toolchain/cache population is a separate explicit step.'
}
[[ ${1:-} != --help ]] || { usage; exit 0; }
[[ $# == 3 ]] || { usage >&2; exit 2; }
[[ $EUID == 0 ]] || { echo 'root required' >&2; exit 1; }
image=$1; size=$2; mirror=$3
[[ $image == /* && $image != *'/../'* && $image != *$'\n'* && $image != *':'* ]] || exit 2
[[ $size =~ ^[0-9]+$ ]] && (( size >= 4096 && size <= 65536 )) || exit 2
[[ $mirror =~ ^https://[A-Za-z0-9./_-]+$ ]] || { echo 'explicit HTTPS mirror required' >&2; exit 2; }
[[ ! -e $image && ! -L $image ]] || { echo 'image already exists; refusing replacement' >&2; exit 1; }
parent=$(dirname -- "$image")
[[ $(realpath -e -- "$parent") == "$parent" ]] || { echo 'canonical existing parent required' >&2; exit 1; }
[[ $(stat -c '%u:%a' -- "$parent") == 0:700 ]] || { echo 'private root-owned parent required' >&2; exit 1; }
: "${TMPDIR:?run-provided scratch required}"
mountpoint=$(mktemp -d "$TMPDIR/swarm-local-image.XXXXXXXX")
log=$(mktemp "$TMPDIR/swarm-local-image-log.XXXXXXXX")
mounted=false
cleanup() {
  code=$?
  trap - EXIT
  if $mounted; then
    if ! umount -- "$mountpoint"; then
      printf 'unmount failed: retained mount at %s\n' "$mountpoint" >&2
      exit 1
    fi
  fi
  rmdir -- "$mountpoint"
  rm -f -- "$log"
  if (( code != 0 )); then
    printf 'provisioning failed; incomplete image retained at %s (do not deploy)\n' "$image" >&2
  fi
  exit "$code"
}
trap cleanup EXIT
umask 077
(set -o noclobber; : > "$image")
truncate -s "${size}M" -- "$image"
mkfs.ext4 -q -F "$image"
mount -o loop,nosuid,nodev "$image" "$mountpoint"
mounted=true
# Package manager runs only in the bounded guest filesystem. Do not start services.
mkdir -p "$mountpoint/usr/sbin"
chmod 755 "$mountpoint/usr" "$mountpoint/usr/sbin"
printf '#!/bin/sh\nexit 101\n' > "$mountpoint/usr/sbin/policy-rc.d"
chmod 755 "$mountpoint/usr/sbin/policy-rc.d"
# Stage deadline is ten minutes; status is emitted every ten seconds.
timeout --signal=TERM --kill-after=15s 600s debootstrap --variant=minbase \
  --include=ca-certificates,git,build-essential,pkg-config,socat,util-linux,libstdc++6,curl,xz-utils \
  resolute "$mountpoint" "$mirror" > "$log" 2>&1 &
pid=$!
while kill -0 "$pid" 2>/dev/null; do
  printf 'local image: bootstrap running\n'
  sleep 10
done
if ! wait "$pid"; then
  tail -n 25 "$log" >&2
  exit 1
fi
chroot "$mountpoint" useradd --system --create-home --home-dir /var/lib/swarmd --shell /usr/sbin/nologin swarm
printf 'local-testbench-base/v1\n' > "$mountpoint/etc/swarm-local-base"
sync -f "$image"
umount -- "$mountpoint"
mounted=false
sha256sum -- "$image"
printf 'Base filesystem created; pinned toolchains and offline caches still required.\n'
