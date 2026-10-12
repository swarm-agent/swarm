#!/usr/bin/env bash
# Swarm's host-change helper: the one program on a headless machine that may
# change the machine for Swarm, installed root-owned by install.sh at
# /usr/local/libexec/swarm-host-change. The daemon (its own user, no new
# privileges) reaches it over the root-owned socket /run/swarm-host-change.sock
# (systemd socket activation runs `serve` as root per connection: one request
# line in, one JSON line out), only after the machine owner approved the change
# through Swarm's API (POST /v1/host/changes, then /approve). It has a fixed
# menu, takes no shell command, quotes everything and logs every call to a
# root-only log. It never restarts Swarm by itself: the daemon asks for that
# as a separate step once it has recorded the result.
#
#   mount-workspace PATH    make PATH (a Git repository in a person's home)
#                           visible and writable to Swarm and its agent
#                           sandboxes at its real path: a systemd drop-in that
#                           binds exactly that folder into the service, and
#                           access for Swarm's user on that folder only (the
#                           owner keeps full rights on anything Swarm creates)
#   unmount-workspace PATH  the reverse
#   restart-daemon          restart Swarm so a new drop-in applies
#   list                    the mounted folders, as JSON
#   serve                   read one "OP [PATH]" line on stdin (the socket) and
#                           answer one JSON line; errors become {"ok":false}
#
# Settings may be overridden through the environment for tests only
# (SWARM_HOST_CHANGE_TEST=1 skips setfacl and systemctl and uses the given
# ROOT prefix for every file it writes).
set -euo pipefail

SERVICE_USER=${SWARM_SERVICE_USER:-swarm}
DAEMON_UNIT=${SWARM_DAEMON_UNIT:-swarm-headless.service}
ROOT=${SWARM_HOST_CHANGE_ROOT:-}
LIST=$ROOT/etc/swarm-headless/workspaces
DROPIN_DIR=$ROOT/etc/systemd/system/$DAEMON_UNIT.d
DROPIN=$DROPIN_DIR/50-workspaces.conf
LOG=$ROOT/var/log/swarm-host-change.log
TEST=${SWARM_HOST_CHANGE_TEST:-0}

SERVE=0
die() {
  log "error: $*"
  if [[ $SERVE == 1 ]]; then printf '{"ok":false,"error":"%s"}\n' "$(json_string "$*")"; exit 0; fi
  echo "$*" >&2; exit 2
}
log() {
  [[ -n $ROOT ]] || install -d -m 0700 /var/log 2>/dev/null || true
  install -d -m 0700 "$(dirname "$LOG")" 2>/dev/null || true
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>"$LOG" 2>/dev/null || true
  chmod 0600 "$LOG" 2>/dev/null || true
}
run() { if [[ $TEST == 1 ]]; then log "skip (test): $*"; else "$@"; fi; }
json_string() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }

# A folder a person's Swarm may work in: absolute, no "..", existing, a
# directory, no symlink in its path, a Git repository, inside a real user's
# home (never the service user's, never Swarm's storage), and that user owns it.
check_path() {
  local path=$1 real owner
  [[ $path == /* ]] || die "path must be absolute"
  [[ $path != *..* && $path != *$'\n'* ]] || die "path must not contain .. or newlines"
  [[ ${#path} -le 512 ]] || die "path is too long"
  [[ -d $path ]] || die "$path is not a directory"
  real=$(realpath -e "$path") || die "$path cannot be resolved"
  [[ $real == "$path" ]] || die "$path goes through a link; use $real"
  [[ -d $path/.git || -f $path/.git ]] || die "$path is not a Git repository"
  case $path in
    /home/*/*) ;;
    *) die "$path is not inside a user's home folder" ;;
  esac
  case $path in
    /home/*/.*|*/.ssh|*/.ssh/*|*/.gnupg|*/.gnupg/*|*/.config/*|*/.local/*) die "$path is a settings or credentials folder" ;;
  esac
  owner=$(stat -c %U "$path")
  [[ $owner != root && $owner != "$SERVICE_USER" ]] || die "$path must belong to a person, not $owner"
  [[ $(id -u "$owner") -ge 1000 ]] || die "$path belongs to a system account"
  [[ $path == /home/$owner/* ]] || die "$path is not inside $owner's home folder"
  OWNER=$owner
}

read_list() { [[ -f $LIST ]] && grep -v '^\s*$' "$LIST" || true; }

write_dropin() {
  local paths
  paths=$(read_list)
  if [[ -z $paths ]]; then
    rm -f "$DROPIN"
    rmdir "$DROPIN_DIR" 2>/dev/null || true
    return
  fi
  install -d -m 0755 "$DROPIN_DIR"
  {
    echo "# Written by swarm-host-change; the owner approved each folder through Swarm."
    echo "[Service]"
    echo "ProtectHome=tmpfs"
    while IFS= read -r p; do
      echo "BindPaths=$p"
      echo "ReadWritePaths=$p"
    done <<<"$paths"
  } >"$DROPIN.tmp"
  chmod 0644 "$DROPIN.tmp"
  mv -f "$DROPIN.tmp" "$DROPIN"
}

# Swarm's user gets rwx on the folder (and x on the folders above it, where
# missing); the owner keeps rwx on everything Swarm creates inside.
grant() {
  local path=$1 owner=$2 parent
  run setfacl -R -m "u:$SERVICE_USER:rwX" "$path"
  run setfacl -R -d -m "u:$SERVICE_USER:rwX" -m "u:$owner:rwX" "$path"
  parent=$(dirname "$path")
  while [[ $parent != / && $parent != /home ]]; do
    if [[ $TEST == 1 ]] || ! runuser -u "$SERVICE_USER" -- test -x "$parent"; then
      run setfacl -m "u:$SERVICE_USER:--x" "$parent"
    fi
    parent=$(dirname "$parent")
  done
}
revoke() {
  local path=$1 owner=$2
  run setfacl -R -x "u:$SERVICE_USER" "$path"
  run setfacl -R -d -x "u:$SERVICE_USER" "$path"
  run setfacl -R -d -x "u:$owner" "$path"
}

mount_workspace() {
  local path=$1 owner
  check_path "$path"
  owner=$OWNER
  install -d -m 0755 "$(dirname "$LIST")"
  if ! read_list | grep -qxF "$path"; then
    printf '%s\n' "$path" >>"$LIST"
    chmod 0644 "$LIST"
  fi
  grant "$path" "$owner"
  write_dropin
  run systemctl daemon-reload
  log "mount-workspace $path (owner $owner)"
  printf '{"ok":true,"op":"mount-workspace","path":"%s","owner":"%s","restart_required":true}\n' "$(json_string "$path")" "$owner"
}

unmount_workspace() {
  local path=$1 owner
  [[ $path == /* && $path != *..* ]] || die "path must be absolute"
  read_list | grep -qxF "$path" || die "$path is not mounted for Swarm"
  owner=$(stat -c %U "$path" 2>/dev/null || true)
  grep -vxF "$path" "$LIST" >"$LIST.tmp" || true
  mv -f "$LIST.tmp" "$LIST"
  [[ -z $owner || ! -d $path ]] || revoke "$path" "$owner"
  write_dropin
  run systemctl daemon-reload
  log "unmount-workspace $path"
  printf '{"ok":true,"op":"unmount-workspace","path":"%s","restart_required":true}\n' "$(json_string "$path")"
}

list_mounts() {
  local sep='' p
  printf '{"ok":true,"mounted":['
  while IFS= read -r p; do
    [[ -n $p ]] || continue
    printf '%s{"path":"%s","exists":%s}' "$sep" "$(json_string "$p")" "$([[ -d $p ]] && echo true || echo false)"
    sep=,
  done < <(read_list)
  printf ']}\n'
}

dispatch() {
  case ${1:-} in
    mount-workspace) [[ $# == 2 ]] || die "usage: mount-workspace PATH"; mount_workspace "$2" ;;
    unmount-workspace) [[ $# == 2 ]] || die "usage: unmount-workspace PATH"; unmount_workspace "$2" ;;
    restart-daemon) [[ $# == 1 ]] || die "usage: restart-daemon"; log "restart-daemon"; run systemctl --no-block restart "$DAEMON_UNIT"; echo '{"ok":true,"op":"restart-daemon"}' ;;
    list) [[ $# == 1 ]] || die "usage: list"; list_mounts ;;
    *) die "unknown operation; the menu is mount-workspace PATH, unmount-workspace PATH, restart-daemon, list" ;;
  esac
}

[[ $TEST == 1 || $(id -u) == 0 ]] || die "run as root"
if [[ ${1:-} == serve ]]; then
  # One request per connection: "OP" or "OP PATH" (the path may hold spaces
  # but never a newline), at most 1024 bytes, then one JSON line back.
  SERVE=1
  IFS= read -r -n 1024 -t 10 line || die "no request"
  op=${line%% *}
  if [[ $line == *" "* ]]; then arg=${line#* }; dispatch "$op" "$arg"; else dispatch "$op"; fi
  exit 0
fi
dispatch "$@"
