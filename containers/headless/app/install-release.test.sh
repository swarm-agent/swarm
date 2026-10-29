#!/usr/bin/env bash
# Hermetic test of the installer's released builds (the default install): it
# downloads the newest release instead of compiling Swarm, checks it, and
# needs no Docker unless environments are on. GitHub, systemd and Docker are
# stand-ins. No network, no root.
# Run: bash install-release.test.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# The installer's functions, without running it.
# shellcheck disable=SC1090
source <(sed '$d' "$here/install.sh")
set +e +o pipefail
set -eu

STATE=$work/state OPT=$work/opt SRC=$work/src CONF=$work/state/install.env STAGE=$work/stage
RELEASE_TARGET=$STATE/release-target UNITS=$work/units
DATA_DIRS=("$work/etc-swarmd") APP_CONFIG=$work/etc-swarmd/headless-app SERVICE_USER=$(id -un) SERVICE_GROUP=$(id -gn)
mkdir -p "$STATE" "$OPT/releases" "$work/github"
log=$work/log
: >"$log"
say() { :; }

# A release on "GitHub": its archive (build-info and the headless package)
# and checksum, as scripts/build-main-dist.sh makes them.
rel_tag=v9.9.9 rel_commit=0123456789abcdef0123456789abcdef01234567
pkg=$work/build/swarm-$rel_tag-linux-amd64
mkdir -p "$pkg/headless/bin" "$pkg/headless/share/sandbox"
printf 'version=%s\ncommit=%s\n' "$rel_tag" "$rel_commit" >"$pkg/build-info.txt"
printf '#!/bin/sh\n' >"$pkg/headless/bin/swarmd" && chmod +x "$pkg/headless/bin/swarmd"
printf '#!/bin/sh\necho new-installer\n' >"$pkg/headless/share/install.sh"
printf '#!/bin/sh\n' >"$pkg/headless/share/sandbox/firewall.sh"
archive=swarm-$rel_tag-linux-amd64.tar.gz
tar -C "$work/build" -czf "$work/github/$archive" "swarm-$rel_tag-linux-amd64"
(cd "$work/github" && sha256sum "$archive" >"$archive.sha256")

# Stand-ins: GitHub's API and downloads; Docker must not be called.
curl() {
  local url=${*: -1} out='' prev=''
  for a in "$@"; do [[ $prev == -o ]] && out=$a; prev=$a; done
  case $url in
    https://api.github.com/repos/swarm-agent/swarm/releases/latest)
      printf '{"tag_name":"%s","target_commitish":"%s"}' "$rel_tag" "$rel_commit" ;;
    https://github.com/swarm-agent/swarm/releases/download/$rel_tag/*)
      cp "$work/github/${url##*/}" "$out" ;;
    *) echo "unexpected curl $url" >&2; return 1 ;;
  esac
}
docker() { echo "docker $*" >>"$log"; return 1; }
systemctl() { echo "systemctl $*" >>"$log"; }

cat >"$CONF" <<EOF
SWARM_REF=main
SOURCE=release
ENVIRONMENTS=off
SANDBOX=off
PERMISSIONS=auto
SECRETS_GATEWAY=off
AI_ACCESS=on
TAILNET=off
AUTO_UPDATE=off
APP_ORIGIN=http://127.0.0.1:8443
APP_DEVICE_NAME=test-box
APP_RELAY_URL=
EOF
load_conf

# 1. The newest release is found and becomes this install's target.
latest_release || fail "latest_release failed"
[[ $(cat "$RELEASE_TARGET") == "$rel_tag $rel_commit" ]] || fail "release target: $(cat "$RELEASE_TARGET")"
[[ $(target_commit) == "$rel_commit" ]] || fail "target_commit: $(target_commit)"
[[ $INSTALLER == "$STATE/install.sh" ]] || fail "a release install's installer is $INSTALLER"

# 2. It is downloaded, checked and unpacked, with no Docker and no build.
build_release "$rel_commit" || fail "build_release failed"
[[ -x $OPT/releases/$rel_commit/bin/swarmd && -f $OPT/releases/$rel_commit/.complete ]] || fail "the release was not unpacked"
[[ -f $OPT/releases/$rel_commit/share/install.sh ]] || fail "the release lost its installer"
! grep -q '^docker' "$log" || fail "Docker was used with environments off: $(grep '^docker' "$log")"
[[ ! -e $STAGE ]] || fail "the download stage was left behind"

# 3. A download that does not match its checksum is refused.
rm -rf "$OPT/releases/$rel_commit"
echo tampered >>"$work/github/$archive"
if (build_release "$rel_commit") 2>/dev/null; then fail "a tampered download was accepted"; fi
[[ ! -e $OPT/releases/$rel_commit ]] || fail "a tampered download was unpacked"
(cd "$work/github" && tar -C "$work/build" -czf "$archive" "swarm-$rel_tag-linux-amd64" && sha256sum "$archive" >"$archive.sha256")

# 4. A release built from another commit than GitHub names is refused.
printf 'version=%s\ncommit=%s\n' "$rel_tag" ffffffffffffffffffffffffffffffffffffffff >"$pkg/build-info.txt"
(cd "$work/github" && tar -C "$work/build" -czf "$archive" "swarm-$rel_tag-linux-amd64" && sha256sum "$archive" >"$archive.sha256")
if (build_release "$rel_commit") 2>/dev/null; then fail "a release from another commit was accepted"; fi
printf 'version=%s\ncommit=%s\n' "$rel_tag" "$rel_commit" >"$pkg/build-info.txt"
(cd "$work/github" && tar -C "$work/build" -czf "$archive" "swarm-$rel_tag-linux-amd64" && sha256sum "$archive" >"$archive.sha256")

# 5. With environments off, Swarm's units need no Docker and there is no
# firewall unit; the secret gateway stays off even if set (it needs them).
build_release "$rel_commit"
ln -sfn "$OPT/releases/$rel_commit" "$OPT/current"
echo SECRETS_GATEWAY=on >>"$CONF"
load_conf
install_units || fail "install_units failed"
unit=$UNITS/$DAEMON_UNIT
[[ -f $unit ]] || fail "no daemon unit"
! grep -q 'docker' "$unit" || fail "the daemon unit needs Docker with environments off: $(grep docker "$unit")"
! grep -q -- '--secrets-gateway=on' "$unit" || fail "the secret gateway is on without environments"
[[ ! -e $UNITS/$FIREWALL_UNIT ]] || fail "a firewall unit was written with environments off"
grep -q "ExecStart=/bin/bash $STATE/install.sh update" "$UNITS/swarm-headless-update.service" || fail "updates do not run the release's installer"
# The timer, and only the timer, marks its update automatic (idle-only).
grep -qx "ExecStart=/bin/bash $STATE/install.sh update --automatic" "$UNITS/swarm-headless-update.service" || fail "the update timer does not mark its update automatic"
# The interval counts from the end of the last run: with OnUnitActiveSec a
# run longer than 5 min (a drain) was followed at once by another.
grep -qx 'OnUnitInactiveSec=5min' "$UNITS/swarm-headless-update.timer" && ! grep -q '^OnUnitActiveSec' "$UNITS/swarm-headless-update.timer" || fail "the update timer does not count from the end of the last run"

# 5b. On a first install current does not exist yet when the units are
# written: the setup page must still name the real release path (Node starts
# it only when its path is the symlink-resolved one).
rm -f "$OPT/current"
install_units "$OPT/releases/$rel_commit" || fail "install_units for a first install failed"
grep -q "^ExecStart=$OPT/releases/$rel_commit/bin/node $OPT/releases/$rel_commit/app/examples/headless-app/server.mjs$" "$UNITS/$APP_UNIT" ||
  fail "the setup page names $(grep ^ExecStart= "$UNITS/$APP_UNIT"), not the release"

# 6. Installs from before released builds keep building from source, with
# environments on (they have Docker).
cat >"$CONF" <<EOF
SWARM_REF=dev
TAILNET=off
PERMISSIONS=auto
SANDBOX=off
AUTO_UPDATE=off
EOF
unset SOURCE ENVIRONMENTS
load_conf
conf_migrate >/dev/null
grep -qx 'SOURCE=git' "$CONF" || fail "an older install was not kept on source builds"
grep -qx 'ENVIRONMENTS=on' "$CONF" || fail "an older install lost its environments"
release_install && fail "an older install became a release install"
environments_on || fail "environments are off for an older install"

echo "PASS: released builds download, are checked, and need no Docker without environments"
