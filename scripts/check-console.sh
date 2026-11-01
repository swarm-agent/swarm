#!/usr/bin/env bash
# Build and test Swarm Console (console/, Rust/GPUI) when a change touches it.
#
# Meant as a workflow `run` step ("in": "environment"): it runs in a fresh
# checkout of the job's head with refs/remotes/origin/dev available. The
# changed files are those between `git merge-base origin/dev HEAD` and HEAD;
# without origin/dev the step's SWARM_BASE (the delivery's base commit) is
# used. With no console change it prints "console: no changes; skipped".
#
# Otherwise, in console/: cargo fmt --check, cargo build --locked --tests,
# cargo test --locked. CARGO_TARGET_DIR is honoured (cargo reads it). On a
# failure the last lines of that command's output are printed to stdout, so a
# workflow can pass {{steps.<id>.output}} back to a coder, and the script
# exits with the command's status. Ends with "CONSOLE CHECK OK" on success.
# A setup error (no checkout, no base, no console/Cargo.toml) is also printed
# to stdout, as "CONSOLE CHECK FAILED: setup: ...", and exits 2.
set -euo pipefail

tail_lines=150

setup_fail() {
  echo "check-console: $*" >&2
  echo "CONSOLE CHECK FAILED: setup: $*"
  exit 2
}

# Any other unexpected failure under set -e would otherwise exit with nothing
# on stdout; name the failing command and line so the step output is never empty.
trap 'status=$?; echo "CONSOLE CHECK FAILED: setup: command failed (exit ${status}) at line ${LINENO}: ${BASH_COMMAND}"; exit 2' ERR

root="$(git rev-parse --show-toplevel 2>/dev/null)" || setup_fail "not inside a Git checkout"
cd "$root"

if git rev-parse --verify --quiet 'refs/remotes/origin/dev^{commit}' >/dev/null; then
  base="$(git merge-base origin/dev HEAD)" || setup_fail "origin/dev and HEAD share no history"
  base_from="merge-base origin/dev HEAD"
elif [ -n "${SWARM_BASE:-}" ]; then
  if ! git rev-parse --verify --quiet "${SWARM_BASE}^{commit}" >/dev/null; then
    setup_fail "SWARM_BASE ${SWARM_BASE} is not a commit in this checkout"
  fi
  base="$SWARM_BASE"
  base_from="SWARM_BASE"
else
  setup_fail "no base to diff against: refs/remotes/origin/dev is missing and SWARM_BASE is not set"
fi

changed="$(git diff --name-only "$base" HEAD)" || setup_fail "git diff --name-only ${base} HEAD failed (its error is on stderr)"
console_changed="$(grep -E '^(console/|Cargo\.(toml|lock)$|rust-toolchain(\.toml)?$)' <<<"$changed" || true)"
if [ -z "$console_changed" ]; then
  echo "console: no changes; skipped"
  exit 0
fi

echo "console: $(wc -l <<<"$console_changed" | tr -d ' ') changed file(s) since ${base} (${base_from}):"
sed -n '1,20p' <<<"$console_changed"
echo "CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-<unset: console/target>}"

if [ ! -f console/Cargo.toml ]; then
  setup_fail "console/Cargo.toml is missing"
fi
cd console

log="$(mktemp -t check-console.XXXXXX)"
trap 'rm -f "$log"' EXIT

step() {
  local name="$1"
  shift
  echo
  echo "==> console: ${name}: $*"
  local status=0
  "$@" >"$log" 2>&1 || status=$?
  if [ "$status" -ne 0 ]; then
    echo "--- last ${tail_lines} lines of: $* ---"
    tail -n "$tail_lines" "$log"
    echo "--- end ---"
    echo "CONSOLE CHECK FAILED: ${name} (exit ${status})"
    exit "$status"
  fi
  tail -n 3 "$log"
}

step "format" cargo fmt --check
step "build" cargo build --locked --tests
step "test" cargo test --locked

echo
echo "CONSOLE CHECK OK"
