#!/usr/bin/env bash
# Explicit provisioning of a trusted base image, not candidate execution.
set -euo pipefail
if [[ ${1:-} == --help ]]; then
  echo 'Usage: bash scripts/testbench-local-tools.sh IMAGE GO_ROOT PNPM_ROOT'
  echo 'Copies only explicit public toolchain distributions, then installs pinned Node in the guest.'
  exit 0
fi
[[ $# == 3 && $EUID == 0 ]] || exit 2
image=$1; goroot=$2; pnpmroot=$3
for p in "$image" "$goroot" "$pnpmroot"; do
  [[ $p == /* && $(realpath -e -- "$p") == "$p" ]] || exit 2
done
[[ -f $image && ! -L $image && $(stat -c '%u' "$image") == 0 ]] || exit 2
[[ $("$goroot/bin/go" version) == 'go version go1.26.7 linux/amd64' ]] || exit 2
[[ -f $pnpmroot/package.json && -f $pnpmroot/bin/pnpm.mjs ]] || exit 2
: "${TMPDIR:?}"
point=$(mktemp -d "$TMPDIR/swarm-local-tools.XXXXXXXX")
mounted=false
cleanup() {
  result=$?; trap - EXIT
  if $mounted; then umount -- "$point" || exit 1; fi
  rmdir -- "$point"
  exit "$result"
}
trap cleanup EXIT
mount -o loop,nosuid,nodev "$image" "$point"
mounted=true
[[ -f $point/etc/swarm-local-base ]] || exit 1
if [[ ! -e $point/opt/go && ! -e $point/opt/pnpm ]]; then
  mkdir -p "$point/opt"
  cp -a -- "$goroot" "$point/opt/go"
  cp -a -- "$pnpmroot" "$point/opt/pnpm"
  chown -R root:root "$point/opt/go" "$point/opt/pnpm"
  ln -s /opt/go/bin/go "$point/usr/local/bin/go"
  ln -s /opt/go/bin/gofmt "$point/usr/local/bin/gofmt"
  ln -s /opt/pnpm/bin/pnpm.mjs "$point/usr/local/bin/pnpm"
else
  [[ $(chroot "$point" /usr/bin/env GOROOT=/opt/go /opt/go/bin/go version) == 'go version go1.26.7 linux/amd64' ]] || exit 1
  cmp -- "$pnpmroot/package.json" "$point/opt/pnpm/package.json" || exit 1
fi
# Apt verifies Ubuntu's signed Release metadata and package hashes; never use trusted=yes.
printf 'deb http://archive.ubuntu.com/ubuntu resolute main universe\n' > "$point/etc/apt/sources.list"
umount -- "$point"; mounted=false
# Trusted distro package provisioning only. No candidate source executes here.
timeout --signal=TERM --kill-after=10s 300s systemd-nspawn --quiet --settings=no --register=no \
  --image="$image" --link-journal=no --timezone=off --as-pid2 /bin/bash -ec '
    export DEBIAN_FRONTEND=noninteractive GOROOT=/opt/go
    chmod 755 /usr /usr/sbin
    apt-get -o APT::Update::Error-Mode=any update -qq
    apt-get install -y --no-install-recommends nodejs=22.22.1+dfsg+~cs22.19.15-1ubuntu1 tzdata
    test "$(go version)" = "go version go1.26.7 linux/amd64"
    test "$(node --version)" = v22.22.1
    test "$(pnpm --version)" = 11.13.1
    printf "pinned-tools-ready\n"
  '
sha256sum -- "$image"
