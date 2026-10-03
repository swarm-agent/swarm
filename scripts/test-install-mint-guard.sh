#!/usr/bin/env bash
# Test-only systemd boundary. CLI paths are fixed; functions accept fixture paths
# so hermetic tests never write host units or inspect host process environments.
set -euo pipefail

mint_guard_fail() {
  printf 'test-install-mint-guard: %s\n' "$1" >&2
  return 1
}

mint_guard_prepare() {
  local directory=$1
  install -d -m 0755 "$directory" || return 1
  # $$ passes a literal dollar through systemd to the shell. Do not reset other
  # ExecStartPre entries: this check must supplement the installed service.
  cat > "$directory/90-no-mint.conf" <<'UNIT' || return 1
[Service]
Environment=SWARM_DISABLE_MINT_REPORT=1
ExecStartPre=/bin/sh -ec 'test "$${SWARM_DISABLE_MINT_REPORT:-}" = 1'
UNIT
  chmod 0644 "$directory/90-no-mint.conf" || return 1
  systemctl daemon-reload || return 1
}

mint_guard_environment() {
  local file=$1 entry count=0
  [[ -r "$file" ]] || { mint_guard_fail 'process environment unavailable'; return 1; }
  # Read only to compare the suppression key; never print process environment.
  while IFS= read -r -d '' entry; do
    case "$entry" in
      SWARM_DISABLE_MINT_REPORT=*)
        [[ "$entry" == SWARM_DISABLE_MINT_REPORT=1 ]] || {
          mint_guard_fail 'process mint suppression is not 1'; return 1;
        }
        count=$((count + 1))
        ;;
    esac
  done < "$file"
  [[ $count == 1 ]] || { mint_guard_fail 'missing or duplicate mint suppression'; return 1; }
}

mint_guard_verify() {
  local proc_root=$1 pid current
  systemctl is-active --quiet swarm.service || return 1
  pid=$(systemctl show -p MainPID --value swarm.service) || return 1
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || { mint_guard_fail 'invalid service MainPID'; return 1; }
  mint_guard_environment "$proc_root/$pid/environ" || return 1
  current=$(systemctl show -p MainPID --value swarm.service) || return 1
  [[ "$current" == "$pid" ]] || { mint_guard_fail 'service changed during verification'; return 1; }
  systemctl is-active --quiet swarm.service || return 1
  printf 'mint_suppression=verified\n'
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  [[ $# == 1 && $(id -u) == 0 && -d /run/systemd/system ]] || {
    mint_guard_fail 'requires root inside the disposable systemd test environment'; exit 1;
  }
  case "$1" in
    prepare) mint_guard_prepare /etc/systemd/system/swarm.service.d ;;
    verify) mint_guard_verify /proc ;;
    *) mint_guard_fail 'expected prepare or verify'; exit 1 ;;
  esac
fi
