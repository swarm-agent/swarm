#!/usr/bin/env bash
# Disposable Ubuntu installer-test image only; never run on the host.
set -euo pipefail

# Keep the official image's repositories and authentication policy. Do not guess
# an address-family or mirror failure from a slow acquisition. Bound each
# connection and disable retries, including the installer's later Git download.
install -m 0644 /dev/stdin /etc/apt/apt.conf.d/99swarm-install-test <<'APT'
Acquire::Retries "0";
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
APT::Update::Error-Mode "any";
APT

run_stage() {
  local stage="$1" budget="$2" result=0 heartbeat_pid
  shift 2
  printf 'ubuntu-bootstrap: stage=%s started budget=%s\n' "$stage" "$budget"
  (
    sleeper=""
    trap 'if [[ -n "$sleeper" ]]; then kill "$sleeper" 2>/dev/null || true; wait "$sleeper" 2>/dev/null || true; fi; exit 0' TERM INT
    while true; do
      sleep 15 & sleeper=$!
      wait "$sleeper"
      printf 'ubuntu-bootstrap: stage=%s running\n' "$stage"
    done
  ) & heartbeat_pid=$!
  timeout --verbose --kill-after=5s "$budget" "$@" || result=$?
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
  if [[ "$result" -ne 0 ]]; then
    printf 'ubuntu-bootstrap: stage=%s failed exit=%s budget=%s; stopping before candidate installation\n' "$stage" "$result" "$budget" >&2
    return "$result"
  fi
  printf 'ubuntu-bootstrap: stage=%s passed\n' "$stage"
}

# Error-Mode=any prevents a partial index refresh from silently succeeding.
# Total deadlines also bound slow trickles that never hit the socket timeout.
run_stage apt-update 180s apt-get update
run_stage apt-install 240s env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl sudo systemd
