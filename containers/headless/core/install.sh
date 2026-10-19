#!/usr/bin/env bash
# Fresh-install only. Stage 2C owns drain, replacement, rollback and updates.
set -euo pipefail
# The fixed layout this installer creates; each system path is declared once.
OPT=/opt/swarm
UNIT=/etc/systemd/system/swarm-headless.service
DATA_DIRS=(/etc/swarmd /var/lib/swarmd /var/cache/swarmd /var/log/swarmd /run/swarmd)
CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
usage() {
  echo 'Usage: bash install.sh --stage-root EMPTY_DESTINATION | --install'
  echo 'Verifies this extracted package. --stage-root writes files only under a new root.'
  echo '--install requires root on a fresh Linux amd64 system with systemd; it does not start services.'
}
if [[ ${1:-} == --help ]]; then usage; exit 0; fi
package=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
bash "$package/verify.sh" "$package"
mode=${1:-}
case "$mode" in
  --stage-root)
    [[ $# == 2 && "$2" == /* && "$2" != / && ! -e "$2" && ! -L "$2" ]] || { usage >&2; exit 2; }
    root=$(realpath -m -- "$2")
    [[ "$root" != / && ! -e "$root" ]] || exit 2
    ;;
  --install)
    [[ $# == 1 && $(id -u) == 0 && $(uname -s)/$(uname -m) == Linux/x86_64 ]] || { usage >&2; exit 2; }
    [[ -d /run/systemd/system ]] || { echo 'A systemd host is required.' >&2; exit 1; }
    root=''
    for command in git jq runuser useradd groupadd getent; do command -v "$command" >/dev/null; done
    command -v bwrap >/dev/null || { echo 'Bubblewrap (bwrap) is required for default agent execution. Install it before installing Swarm.' >&2; exit 1; }
    [[ -s $CA_BUNDLE ]] || { echo 'Install CA certificates first.' >&2; exit 1; }
    ;;
  *) usage >&2; exit 2 ;;
esac
# Refuse existing installs and data before any mutation. Never adopt or stop a
# live daemon, create update timers, install dependencies or change networking.
for owned in "$OPT" /usr/local/bin/swarmctl "$UNIT" /usr/local/lib/systemd/system/swarm-headless.service "${DATA_DIRS[@]}" /var/lib/swarm; do
  [[ ! -e "$root$owned" && ! -L "$root$owned" ]] || { echo "Existing Swarm path; fresh install refused: $owned" >&2; exit 1; }
done
if [[ $mode == --install ]]; then
  if getent passwd swarm >/dev/null || getent group swarm >/dev/null; then
    echo 'Existing swarm account/group; fresh install refused.' >&2; exit 1
  fi
  # Loader validation before users/files are installed. The package's FFF
  # library is used, never a source checkout or installed Swarm library.
  for binary in swarmd swarmctl swarm-fff-search; do
    report=$(LD_LIBRARY_PATH="$package/lib" ldd "$package/bin/$binary")
    [[ "$report" != *'not found'* ]] || { echo 'Required shared library missing.' >&2; exit 1; }
  done
  groupadd --system swarm
  useradd --system --gid swarm --home-dir /var/lib/swarm --shell /usr/sbin/nologin swarm
fi
version=$(jq -r .version "$package/manifest.json")
release="$root$OPT/releases/$version"
install -d -m 0755 "$release" "$root/usr/local/bin" "$root${UNIT%/*}"
cp -R -- "$package/bin" "$package/lib" "$package/doc" "$release/"
install -m 0644 "$package/manifest.json" "$package/SHA256SUMS" "$release/"
ln -s "releases/$version" "$root$OPT/current"
install -m 0644 "$package/systemd/swarm-headless.service" "$root$UNIT"
# The private owner socket is accessed as the daemon account, not exposed on TCP.
cat > "$root/usr/local/bin/swarmctl" <<WRAPPER
#!/bin/sh
set -eu
export LD_LIBRARY_PATH=$OPT/current/lib
if [ "\$(id -u)" = 0 ]; then
  exec runuser -u swarm -- $OPT/current/bin/swarmctl "\$@"
fi
exec $OPT/current/bin/swarmctl "\$@"
WRAPPER
chmod 0755 "$root/usr/local/bin/swarmctl"
install -d -m 0700 "$root/var/lib/swarm"
if [[ $mode == --install ]]; then
  chown swarm:swarm /var/lib/swarm
  echo 'Files installed; service has NOT been started. Review doc/HEADLESS.md, then explicitly activate the service.'
else
  echo 'PASS: fresh installation files staged; no account, service or network changes.'
fi
