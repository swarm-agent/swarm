#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-fetch-session-db-dump.sh <ssh-alias> <session-id-or-url> [options]

Stops Swarm on the remote host, runs the checked-in local session DB inspector
against the inactive remote Pebble DB, streams the JSON dump into local TMPDIR,
removes the remote dump, and leaves Swarm stopped.

Options:
  --remote-dir <path>      Remote swarm-go checkout; auto-discovered by default.
  --service <unit>         Remote service unit. Default: swarm.service
  --db-path <path>         Remote Pebble DB. Default: /var/lib/swarmd/swarmd.pebble
  --remote-tmpdir <path>   Remote disposable directory. Defaults to remote TMPDIR,
                           XDG_RUNTIME_DIR, or /run/user/<uid>; fails if none works.
  -h, --help               Show this help.

The resulting local JSON path is printed to stdout. Progress is printed to stderr.
USAGE
}

fail() {
  printf 'ssh-fetch-session-db-dump: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

extract_session_id() {
  local value="$1"
  value="${value%%#*}"
  value="${value%%\?*}"
  value="${value%/}"
  value="${value##*/}"
  printf '%s' "$value"
}

safe_filename() {
  local value="$1"
  value="$(printf '%s' "$value" | tr -c 'A-Za-z0-9_.-' '_')"
  value="${value:0:80}"
  [[ -n "$value" ]] || value="session"
  printf '%s' "$value"
}

b64() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

if [[ $# -lt 2 ]]; then
  usage
  exit 2
fi

SSH_ALIAS="$1"
SESSION_INPUT="$2"
shift 2

REMOTE_DIR=""
SERVICE_UNIT="swarm.service"
DB_PATH="/var/lib/swarmd/swarmd.pebble"
REMOTE_TMPDIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remote-dir)
      [[ $# -ge 2 ]] || fail "--remote-dir requires a value"
      REMOTE_DIR="$2"
      shift 2
      ;;
    --service)
      [[ $# -ge 2 ]] || fail "--service requires a value"
      SERVICE_UNIT="$2"
      shift 2
      ;;
    --db-path)
      [[ $# -ge 2 ]] || fail "--db-path requires a value"
      DB_PATH="$2"
      shift 2
      ;;
    --remote-tmpdir)
      [[ $# -ge 2 ]] || fail "--remote-tmpdir requires a value"
      REMOTE_TMPDIR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

SESSION_ID="$(extract_session_id "$SESSION_INPUT")"
[[ -n "$SSH_ALIAS" ]] || fail "empty SSH alias"
[[ -n "$SESSION_ID" ]] || fail "could not extract a session id"
[[ "$SESSION_ID" =~ ^[A-Za-z0-9._:-]+$ ]] || fail "session id contains unsupported characters"
[[ ${#SESSION_ID} -le 200 ]] || fail "session id is too long"
[[ -n "$SERVICE_UNIT" ]] || fail "empty service unit"
[[ -n "$DB_PATH" ]] || fail "empty database path"

require_command ssh
require_command base64
require_command mktemp

[[ -n "${TMPDIR:-}" ]] || fail "TMPDIR must be set for the local dump"
[[ -d "$TMPDIR" && -w "$TMPDIR" ]] || fail "TMPDIR is not a writable directory: $TMPDIR"

umask 077
safe_session="$(safe_filename "$SESSION_ID")"
LOCAL_OUTPUT="$(mktemp --tmpdir="$TMPDIR" "swarm-sessiondump-${safe_session}.XXXXXX.json")"
completed="false"
cleanup_local_partial() {
  if [[ "$completed" != "true" && -n "${LOCAL_OUTPUT:-}" && -f "$LOCAL_OUTPUT" ]]; then
    rm -f -- "$LOCAL_OUTPUT"
  fi
}
trap cleanup_local_partial EXIT

printf 'ssh-fetch-session-db-dump: remote=%s session=%s service=%s db=%s\n' \
  "$SSH_ALIAS" "$SESSION_ID" "$SERVICE_UNIT" "$DB_PATH" >&2

ssh "$SSH_ALIAS" 'bash -s' -- \
  "remote_dir_b64=$(b64 "$REMOTE_DIR")" \
  "service_unit_b64=$(b64 "$SERVICE_UNIT")" \
  "db_path_b64=$(b64 "$DB_PATH")" \
  "remote_tmpdir_b64=$(b64 "$REMOTE_TMPDIR")" \
  "session_id_b64=$(b64 "$SESSION_ID")" >"$LOCAL_OUTPUT" <<'REMOTE_FETCH_SESSION_DUMP'
set -euo pipefail
umask 077

decode_b64() {
  if [[ -n "$1" ]]; then
    printf '%s' "$1" | base64 -d
  fi
}

remote_dir=""
service_unit="swarm.service"
db_path="/var/lib/swarmd/swarmd.pebble"
remote_tmpdir=""
session_id=""
for arg in "$@"; do
  case "$arg" in
    remote_dir_b64=*) remote_dir="$(decode_b64 "${arg#remote_dir_b64=}")" ;;
    service_unit_b64=*) service_unit="$(decode_b64 "${arg#service_unit_b64=}")" ;;
    db_path_b64=*) db_path="$(decode_b64 "${arg#db_path_b64=}")" ;;
    remote_tmpdir_b64=*) remote_tmpdir="$(decode_b64 "${arg#remote_tmpdir_b64=}")" ;;
    session_id_b64=*) session_id="$(decode_b64 "${arg#session_id_b64=}")" ;;
  esac
done

if [[ -z "$remote_dir" ]]; then
  for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
    if [[ -d "$candidate/.git" && -f "$candidate/AGENTS.md" && -x "$candidate/scripts/local-session-db-inspect.sh" ]]; then
      remote_dir="$candidate"
      break
    fi
  done
fi
if [[ -z "$remote_dir" ]]; then
  while IFS= read -r candidate; do
    if [[ -d "$candidate/.git" && -f "$candidate/AGENTS.md" && -x "$candidate/scripts/local-session-db-inspect.sh" ]]; then
      remote_dir="$candidate"
      break
    fi
  done < <(find "$HOME" /opt /srv -maxdepth 4 -type d -name swarm-go 2>/dev/null)
fi
[[ -n "$remote_dir" ]] || { printf 'ssh-fetch-session-db-dump: remote swarm-go checkout not found; pass --remote-dir\n' >&2; exit 1; }
inspector="$remote_dir/scripts/local-session-db-inspect.sh"
[[ -x "$inspector" ]] || { printf 'ssh-fetch-session-db-dump: inspector is missing or not executable: %s\n' "$inspector" >&2; exit 1; }
[[ -d "$db_path" ]] || { printf 'ssh-fetch-session-db-dump: database directory not found: %s\n' "$db_path" >&2; exit 1; }

user_before="$(systemctl --user is-active "$service_unit" 2>/dev/null || true)"
system_before="$(systemctl is-active "$service_unit" 2>/dev/null || true)"
printf 'ssh-fetch-session-db-dump: service before stop: user=%s system=%s\n' "${user_before:-unknown}" "${system_before:-unknown}" >&2
if [[ "$user_before" == "active" ]]; then
  systemctl --user stop "$service_unit"
fi
if [[ "$system_before" == "active" ]]; then
  systemctl stop "$service_unit" 2>/dev/null || sudo -n systemctl stop "$service_unit"
fi
user_after="$(systemctl --user is-active "$service_unit" 2>/dev/null || true)"
system_after="$(systemctl is-active "$service_unit" 2>/dev/null || true)"
if [[ "$user_after" == "active" || "$system_after" == "active" ]]; then
  printf 'ssh-fetch-session-db-dump: refusing DB inspection while service remains active: user=%s system=%s\n' "$user_after" "$system_after" >&2
  exit 1
fi
printf 'ssh-fetch-session-db-dump: service left stopped: user=%s system=%s\n' "${user_after:-unknown}" "${system_after:-unknown}" >&2

if [[ -z "$remote_tmpdir" ]]; then
  for candidate in "${TMPDIR:-}" "${XDG_RUNTIME_DIR:-}" "/run/user/$(id -u)"; do
    if [[ -n "$candidate" && -d "$candidate" && -w "$candidate" ]]; then
      remote_tmpdir="$candidate"
      break
    fi
  done
fi
[[ -n "$remote_tmpdir" && -d "$remote_tmpdir" && -w "$remote_tmpdir" ]] || {
  printf 'ssh-fetch-session-db-dump: no writable remote disposable directory; pass --remote-tmpdir\n' >&2
  exit 1
}

safe_session="$(printf '%s' "$session_id" | tr -c 'A-Za-z0-9_.-' '_')"
safe_session="${safe_session:0:80}"
[[ -n "$safe_session" ]] || safe_session="session"
remote_output="$(mktemp "${remote_tmpdir%/}/swarm-sessiondump-${safe_session}.XXXXXX.json")"
cleanup_remote() {
  if [[ -n "${remote_output:-}" && -f "$remote_output" ]]; then
    rm -f -- "$remote_output"
  fi
}
trap cleanup_remote EXIT

printf 'ssh-fetch-session-db-dump: checkout=%s remote_tmpdir=%s\n' "$remote_dir" "$remote_tmpdir" >&2
TMPDIR="$remote_tmpdir" "$inspector" \
  --db-path "$db_path" \
  --session "$session_id" \
  --dump \
  --json \
  --out "$remote_output" >&2
[[ -s "$remote_output" ]] || { printf 'ssh-fetch-session-db-dump: remote inspector produced an empty dump\n' >&2; exit 1; }
cat -- "$remote_output"
REMOTE_FETCH_SESSION_DUMP

[[ -s "$LOCAL_OUTPUT" ]] || fail "remote inspector produced an empty local dump"
completed="true"
trap - EXIT
printf 'ssh-fetch-session-db-dump: wrote local dump %s (%s bytes); Swarm remains stopped on %s\n' \
  "$LOCAL_OUTPUT" "$(wc -c <"$LOCAL_OUTPUT" | tr -d ' ')" "$SSH_ALIAS" >&2
printf '%s\n' "$LOCAL_OUTPUT"
