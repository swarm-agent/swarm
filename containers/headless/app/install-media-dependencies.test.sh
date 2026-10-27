#!/usr/bin/env bash
# Purpose: host transcription requires both ffmpeg and ffprobe on fresh install
# and update. Exercise install_media_dependencies and finish_update with package
# manager stand-ins: missing tools must install, failures must stop before any
# release work, and already-present tools must not mutate host packages. This
# function-level harness is the narrowest layer proving those decisions without
# root, network, package installation or a running daemon.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# shellcheck disable=SC1090
source <(sed '$d' "$here/install.sh")
fail() { echo "FAIL: $*" >&2; exit 1; }
OPT=$work/opt
log=$work/log
command() {
  if [[ $1 == -v && ( $2 == ffmpeg || $2 == ffprobe ) ]]; then
    [[ :$PATH: == *:/usr/bin:* ]] || fail 'daemon tool directory missing'
    [[ -f $work/$2 ]]
  else
    builtin command "$@"
  fi
}
apt-get() {
  echo "$*" >>"$log"
  [[ ${apt_failure:-} != "$1" ]] || return 1
  if [[ $1 == install && ${omit_tools:-0} == 0 ]]; then
    [[ $DEBIAN_FRONTEND == noninteractive ]] || fail 'interactive install'
    touch "$work/ffmpeg" "$work/ffprobe"
  fi
}
for scenario in absent present missing-probe update-fails install-fails missing-after-install; do
  (
    : >"$log"
    # Each case gets independent tool-presence markers.
    work=$work/$scenario
    mkdir -p "$work"
    case $scenario in
      present) touch "$work/ffmpeg" "$work/ffprobe" ;;
      missing-probe) touch "$work/ffmpeg" ;;
      update-fails) apt_failure=update ;;
      install-fails) apt_failure=install ;;
      missing-after-install) omit_tools=1 ;;
    esac
    rc=0
    install_media_dependencies || rc=$?
    case $scenario in
      present) [[ $rc == 0 && ! -s $log ]] || fail 'present tools changed packages' ;;
      absent|missing-probe)
        [[ $rc == 0 && -f $work/ffmpeg && -f $work/ffprobe ]] || fail "$scenario not repaired"
        [[ $(cat "$log") == $'update -qq\ninstall -y -qq ffmpeg' ]] || fail 'wrong apt commands' ;;
      *) [[ $rc != 0 ]] || fail "$scenario silently succeeded" ;;
    esac
    if [[ $scenario == update-fails ]]; then
      [[ $(cat "$log") == 'update -qq' ]] || fail 'installed after refresh failure'
    fi
  )
done
# Even an already-current update repairs dependencies before its early return.
load_conf() { :; }
target_commit() { echo same; }
running_commit() { echo same; }
release_install() { return 1; }
update_state() { echo state >>"$log"; }
SWARM_REF=dev
: >"$log"
finish_update
[[ $(cat "$log") == $'update -qq\ninstall -y -qq ffmpeg\nstate' ]] || fail 'current update skipped dependencies'
# Failed package installation cannot advance update state or inspect releases.
install_media_dependencies() { return 1; }
target_commit() { echo unexpected >>"$log"; }
: >"$log"
if finish_update; then fail 'failed dependencies accepted'; fi
[[ ! -s $log ]] || fail 'update continued after dependency failure'
DEV_SOURCE=test-source
if update; then fail 'update hid dependency failure'; fi
[[ ! -s $log ]] || fail 'update continued after dependency failure'
# Fresh-install wiring and the generated systemd PATH are installer contracts.
grep -q '^  install_media_dependencies$' "$here/install.sh" || fail 'fresh install missing dependency hook'
grep -q '^Environment=PATH=.*:/usr/bin:' "$here/install.sh" || fail 'unit cannot find apt binaries'
echo 'PASS: host media dependencies'
